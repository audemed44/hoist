// Package sleeping asks Gatehouse which containers it has stopped on
// purpose (scale-to-zero), so Hoist shows them as asleep rather than down
// or waiting to be started, and a deploy doesn't wake them.
package sleeping

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// cacheFor keeps the stacks page from asking Gatehouse on every refresh.
const cacheFor = 20 * time.Second

type Client struct {
	URL   string // Gatehouse's admin port, e.g. http://host.docker.internal:9140
	Token string // its discovery token

	http *http.Client
	mu   sync.Mutex
	at   time.Time
	last map[string]string
}

func New(url, token string) *Client {
	return &Client{URL: strings.TrimRight(url, "/"), Token: token, http: &http.Client{Timeout: 5 * time.Second}}
}

// Asleep returns container name → state (sleeping, waking or stopping)
// for every app Gatehouse has stopped on purpose. If Gatehouse can't be
// reached, the last answer is used. A nil client knows of none.
func (c *Client) Asleep(ctx context.Context) map[string]string {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.last != nil && time.Since(c.at) < cacheFor {
		return c.last
	}
	got, err := c.fetch(ctx)
	if err != nil {
		slog.Debug("gatehouse", "err", err)
		return c.last
	}
	c.last, c.at = got, time.Now()
	return got
}

func (c *Client) fetch(ctx context.Context) (map[string]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL+"/api/discovery", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gatehouse answered HTTP %d", resp.StatusCode)
	}
	var d struct {
		Hosts []struct {
			Container string `json:"container"`
			IdleStop  string `json:"idle_stop"`
			State     string `json:"state"`
		} `json:"hosts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, h := range d.Hosts {
		if h.Container != "" && h.IdleStop != "" && h.State != "" && h.State != "awake" {
			out[h.Container] = h.State
		}
	}
	return out, nil
}
