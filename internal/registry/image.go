package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

// ImageConfig is what an image says about itself, read from the registry
// without pulling it.
type ImageConfig struct {
	// Ports the image exposes, e.g. "80/tcp".
	Ports []string
	// Volumes the image declares, e.g. "/data".
	Volumes     []string
	Healthcheck bool
}

// Config fetches an image's config: its manifest (this platform's, from a
// multi-platform index) and the config blob it points to.
func (c *Client) Config(ctx context.Context, r Ref) (ImageConfig, error) {
	ref := r.Tag
	if r.Digest != "" {
		ref = r.Digest
	}
	var m struct {
		Manifests []struct {
			Digest   string `json:"digest"`
			Platform struct {
				OS           string `json:"os"`
				Architecture string `json:"architecture"`
			} `json:"platform"`
		} `json:"manifests"`
		Config struct {
			Digest string `json:"digest"`
		} `json:"config"`
	}
	if err := c.getJSON(ctx, r, "/manifests/"+ref, manifestTypes, &m); err != nil {
		return ImageConfig{}, err
	}
	if len(m.Manifests) > 0 {
		pick := ""
		for _, d := range m.Manifests {
			if d.Platform.OS == "linux" && (pick == "" || d.Platform.Architecture == runtime.GOARCH) {
				pick = d.Digest
			}
		}
		if pick == "" {
			return ImageConfig{}, errors.New("no linux image in the index")
		}
		if err := c.getJSON(ctx, r, "/manifests/"+pick, manifestTypes, &m); err != nil {
			return ImageConfig{}, err
		}
	}
	if m.Config.Digest == "" {
		return ImageConfig{}, errors.New("the manifest has no config")
	}
	var blob struct {
		Config struct {
			ExposedPorts map[string]struct{}
			Volumes      map[string]struct{}
			Healthcheck  *struct{ Test []string }
		} `json:"config"`
	}
	if err := c.getJSON(ctx, r, "/blobs/"+m.Config.Digest, "*/*", &blob); err != nil {
		return ImageConfig{}, err
	}
	out := ImageConfig{Ports: []string{}, Volumes: []string{}}
	for p := range blob.Config.ExposedPorts {
		out.Ports = append(out.Ports, p)
	}
	sort.Slice(out.Ports, func(i, j int) bool {
		a, _ := strconv.Atoi(strings.Split(out.Ports[i], "/")[0])
		b, _ := strconv.Atoi(strings.Split(out.Ports[j], "/")[0])
		return a < b
	})
	for v := range blob.Config.Volumes {
		out.Volumes = append(out.Volumes, v)
	}
	sort.Strings(out.Volumes)
	h := blob.Config.Healthcheck
	out.Healthcheck = h != nil && len(h.Test) > 0 && h.Test[0] != "NONE"
	return out, nil
}

func (c *Client) getJSON(ctx context.Context, r Ref, path, accept string, v any) error {
	resp, err := c.do(ctx, r, "GET", path, accept)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// Pinnable picks the version tag to pin a new image to: the newest of the
// most common plain shape (1.2.3 rather than 1.2.3-rc1 or 1.2.3-alpine),
// or of the most common shape when no tag is plain. "" when no tag looks
// like a version.
func Pinnable(tags []string) string {
	type group struct {
		plain  bool
		newest Version
		count  int
	}
	groups := map[string]*group{}
	for _, t := range tags {
		v, ok := ParseVersion(t)
		if !ok {
			continue
		}
		key := fmt.Sprintf("%s|%d|%s|%v", v.Prefix, len(v.Nums), v.Suffix, v.Build >= 0)
		g := groups[key]
		if g == nil {
			g = &group{plain: v.Suffix == "" && v.Build < 0, newest: v}
			groups[key] = g
		}
		g.count++
		if v.compare(g.newest) > 0 {
			g.newest = v
		}
	}
	var best *group
	for _, g := range groups {
		switch {
		case best == nil,
			g.plain && !best.plain,
			g.plain == best.plain && g.count > best.count,
			g.plain == best.plain && g.count == best.count && g.newest.Tag > best.newest.Tag:
			best = g
		}
	}
	if best == nil {
		return ""
	}
	return best.newest.Tag
}
