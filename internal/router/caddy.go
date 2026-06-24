// Package router implements Mac-side hostname routing via the Caddy
// admin API plus /etc/hosts resolution (snippet print or localias).
package router

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// DefaultCaddyURL is where Caddy's admin API listens by default.
const DefaultCaddyURL = "http://localhost:2019"

// Client is a thin caddy admin-API client.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// New returns a Client targeting DefaultCaddyURL.
func New() *Client { return NewWithURL(DefaultCaddyURL) }

// NewWithURL returns a Client targeting the given URL.
func NewWithURL(url string) *Client {
	return &Client{baseURL: url, httpClient: &http.Client{Timeout: 5 * time.Second}}
}

// do issues an HTTP request and returns (status, body, err). Transport
// errors return a typed message that points the user at brew install.
func (c *Client) do(method, path string, body any) (int, string, error) {
	var reqBody io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return 0, "", fmt.Errorf("marshal request body: %w", err)
		}
		reqBody = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(method, c.baseURL+path, reqBody)
	if err != nil {
		return 0, "", err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf(
			"caddy admin API not reachable at %s:\n  reason: %v\n\n"+
				"Start Caddy as a service:\n  brew install caddy\n  sudo brew services start caddy",
			c.baseURL, err)
	}
	defer resp.Body.Close()
	bs, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(bs), nil
}
