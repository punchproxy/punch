package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// errAPINotFound is returned by doRequest when the server responds with
// HTTP 404. Resource-specific commands wrap it into a friendlier error.
var errAPINotFound = errors.New("api: not found")

// apiURL builds a request URL from a base address and an API path. It
// tolerates bare host:port forms by defaulting to http://.
func apiURL(base, path string) (string, error) {
	if !strings.Contains(base, "://") {
		base = "http://" + base
	}
	parsed, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("parse addr: %w", err)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("invalid addr %q", base)
	}
	apiPath, err := url.Parse(path)
	if err != nil {
		return "", fmt.Errorf("parse API path: %w", err)
	}
	joinedPath := strings.TrimRight(parsed.Path, "/") + apiPath.Path
	unescapedPath, err := url.PathUnescape(joinedPath)
	if err != nil {
		return "", fmt.Errorf("parse API path: %w", err)
	}
	parsed.Path = unescapedPath
	parsed.RawPath = joinedPath
	parsed.RawQuery = apiPath.RawQuery
	parsed.Fragment = ""
	return parsed.String(), nil
}

// doRequest sends a request to the Punch API and returns the response
// when its status code is in accept. The caller owns and must close the
// returned response body.
//
// 404 responses are reported as errAPINotFound so callers can map them to
// command-specific "not found" errors. Any other unexpected status returns
// an error including up to 4KiB of the response body for diagnostics.
//
// doRequest does NOT impose its own deadline; the caller should pass a
// context with a timeout (typically derived from cfg.timeout).
func doRequest(ctx context.Context, cfg commandConfig, method, endpoint string, body io.Reader, accept ...int) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if cfg.token != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.token)
	}

	resp, err := cfg.client.Do(req)
	if err != nil {
		return nil, err
	}
	for _, code := range accept {
		if resp.StatusCode == code {
			return resp, nil
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, errAPINotFound
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	msg := strings.TrimSpace(string(raw))
	if msg == "" {
		msg = resp.Status
	}
	return nil, fmt.Errorf("API returned %s: %s", resp.Status, msg)
}
