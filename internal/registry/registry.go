// Package registry asks image registries what a tag points to and which tags
// exist, without pulling anything: a manifest HEAD and the tag list, over the
// Docker registry HTTP API v2 with anonymous tokens.
package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Ref is a parsed image reference.
type Ref struct {
	Registry string // e.g. registry-1.docker.io, ghcr.io
	Repo     string // e.g. library/nginx, audemed44/hoist
	Tag      string // "" when pinned by digest
	Digest   string // set when the reference has @sha256:…
}

// Name is the repository as docker shows it (docker.io images without the
// registry), for matching RepoDigests.
func (r Ref) Name() string {
	if r.Registry == dockerHub {
		return strings.TrimPrefix(r.Repo, "library/")
	}
	return r.Registry + "/" + r.Repo
}

const dockerHub = "registry-1.docker.io"

// Parse splits an image reference the way docker does.
func Parse(ref string) (Ref, error) {
	var r Ref
	if ref == "" || strings.ContainsAny(ref, " \t") {
		return r, fmt.Errorf("invalid image %q", ref)
	}
	if name, digest, ok := strings.Cut(ref, "@"); ok {
		ref, r.Digest = name, digest
	}
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		ref, r.Tag = ref[:i], ref[i+1:]
	}
	if r.Tag == "" && r.Digest == "" {
		r.Tag = "latest"
	}
	first, rest, ok := strings.Cut(ref, "/")
	if ok && (strings.ContainsAny(first, ".:") || first == "localhost") {
		r.Registry, r.Repo = first, rest
	} else {
		r.Registry, r.Repo = dockerHub, ref
		if !ok {
			r.Repo = "library/" + ref
		}
	}
	if r.Registry == "docker.io" || r.Registry == "index.docker.io" {
		r.Registry = dockerHub
		if !strings.Contains(r.Repo, "/") {
			r.Repo = "library/" + r.Repo
		}
	}
	return r, nil
}

// Client talks to registries, caching anonymous tokens per repository.
type Client struct {
	http *http.Client

	mu     sync.Mutex
	tokens map[string]token
}

type token struct {
	value   string
	expires time.Time
}

func New() *Client {
	return &Client{http: &http.Client{Timeout: 20 * time.Second}, tokens: map[string]token{}}
}

// ErrNotFound means the registry has no such repository or tag.
var ErrNotFound = errors.New("not found in the registry")

var manifestTypes = strings.Join([]string{
	"application/vnd.oci.image.index.v1+json",
	"application/vnd.docker.distribution.manifest.list.v2+json",
	"application/vnd.docker.distribution.manifest.v2+json",
	"application/vnd.oci.image.manifest.v1+json",
}, ", ")

// Digest returns the digest a tag points to now (the manifest list's, for
// multi-platform images, which is what docker records after a pull).
func (c *Client) Digest(ctx context.Context, r Ref) (string, error) {
	resp, err := c.do(ctx, r, http.MethodHead, "/manifests/"+r.Tag, manifestTypes)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	d := resp.Header.Get("Docker-Content-Digest")
	if d == "" {
		return "", errors.New("the registry didn't say the digest")
	}
	return d, nil
}

// maxTagPages bounds the tag list (100 tags a page on Docker Hub).
const maxTagPages = 20

var nextRe = regexp.MustCompile(`<([^>]+)>;\s*rel="?next"?`)

// Tags lists a repository's tags.
func (c *Client) Tags(ctx context.Context, r Ref) ([]string, error) {
	var all []string
	path := "/tags/list?n=1000"
	for page := 0; page < maxTagPages && path != ""; page++ {
		resp, err := c.do(ctx, r, http.MethodGet, path, "application/json")
		if err != nil {
			return all, err
		}
		var body struct {
			Tags []string `json:"tags"`
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&body)
		resp.Body.Close()
		if err != nil {
			return all, fmt.Errorf("tag list: %w", err)
		}
		all = append(all, body.Tags...)
		path = ""
		if m := nextRe.FindStringSubmatch(resp.Header.Get("Link")); m != nil {
			if u, err := url.Parse(m[1]); err == nil {
				path = strings.TrimPrefix(u.Path, "/v2/"+r.Repo)
				if u.RawQuery != "" {
					path += "?" + u.RawQuery
				}
			}
		}
	}
	return all, nil
}

func (c *Client) do(ctx context.Context, r Ref, method, path, accept string) (*http.Response, error) {
	u := "https://" + r.Registry + "/v2/" + r.Repo + path
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", accept)
		if t := c.cachedToken(r); t != "" {
			req.Header.Set("Authorization", "Bearer "+t)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, fmt.Errorf("could not reach %s", r.Registry)
		}
		switch {
		case resp.StatusCode == http.StatusUnauthorized && attempt == 0:
			challenge := resp.Header.Get("WWW-Authenticate")
			resp.Body.Close()
			if err := c.authenticate(ctx, r, challenge); err != nil {
				return nil, err
			}
			continue
		case resp.StatusCode == http.StatusNotFound:
			resp.Body.Close()
			return nil, ErrNotFound
		case resp.StatusCode == http.StatusTooManyRequests:
			resp.Body.Close()
			return nil, fmt.Errorf("%s is rate limiting; try later", r.Registry)
		case resp.StatusCode >= 300:
			resp.Body.Close()
			return nil, fmt.Errorf("%s answered HTTP %d", r.Registry, resp.StatusCode)
		}
		return resp, nil
	}
	return nil, fmt.Errorf("%s wants a login (private image?)", r.Registry)
}

func (c *Client) cachedToken(r Ref) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := c.tokens[r.Registry+"/"+r.Repo]
	if time.Now().After(t.expires) {
		return ""
	}
	return t.value
}

var paramRe = regexp.MustCompile(`(\w+)="([^"]*)"`)

// authenticate gets an anonymous pull token as the challenge describes:
// Bearer realm="…",service="…",scope="…".
func (c *Client) authenticate(ctx context.Context, r Ref, challenge string) error {
	scheme, params, _ := strings.Cut(challenge, " ")
	if !strings.EqualFold(scheme, "Bearer") {
		return fmt.Errorf("%s wants a login (private image?)", r.Registry)
	}
	p := map[string]string{}
	for _, m := range paramRe.FindAllStringSubmatch(params, -1) {
		p[m[1]] = m[2]
	}
	realm, err := url.Parse(p["realm"])
	if err != nil || realm.Scheme != "https" {
		return fmt.Errorf("%s: unexpected auth challenge", r.Registry)
	}
	q := realm.Query()
	if p["service"] != "" {
		q.Set("service", p["service"])
	}
	scope := p["scope"]
	if scope == "" {
		scope = "repository:" + r.Repo + ":pull"
	}
	q.Set("scope", scope)
	realm.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, realm.String(), nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach %s", realm.Host)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s wants a login (private image?)", r.Registry)
	}
	var body struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return err
	}
	t := body.Token
	if t == "" {
		t = body.AccessToken
	}
	ttl := time.Duration(body.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = 60 * time.Second
	}
	c.mu.Lock()
	c.tokens[r.Registry+"/"+r.Repo] = token{value: t, expires: time.Now().Add(ttl - 10*time.Second)}
	c.mu.Unlock()
	return nil
}
