// Package mcp implements the MCP (Model Context Protocol) server for tasks127.
//
// The MCP server is a thin translation layer in front of the main tasks127
// REST API. It does not talk to the database directly. That way every MCP
// tool call inherits the REST API's auth, visibility scoping, audit logging,
// and subscription firing without needing to re-implement any of it.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Client is a small HTTP wrapper around the tasks127 REST API.
// It is not the general-purpose client a third party would use; it exists
// to keep the MCP tool handlers short and uniform.
type Client struct {
	BaseURL string // e.g. http://127.0.0.1:8080
	APIKey  string // bearer token
	HTTP    *http.Client
}

// NewClient builds a Client with sensible defaults for local use.
func NewClient(baseURL, apiKey string) *Client {
	return &Client{
		BaseURL: baseURL,
		APIKey:  apiKey,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
}

// APIError wraps a non-2xx response from the tasks127 REST API.
// The MCP handlers let these bubble up unchanged; the SDK will format them
// into a tool-error response for the agent.
type APIError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *APIError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("tasks127 API error (%d %s): %s", e.StatusCode, e.Code, e.Message)
	}
	return fmt.Sprintf("tasks127 API error (%d): %s", e.StatusCode, e.Message)
}

// do makes an HTTP request and decodes the JSON response into out if provided.
// On non-2xx it returns an *APIError with the server-provided code/message.
func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal body: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, bodyReader)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		apiErr := &APIError{StatusCode: resp.StatusCode, Message: string(respBody)}
		// Try to parse the standard error envelope: {"error":{"code":"...","message":"..."}}
		var parsed struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(respBody, &parsed) == nil && parsed.Error.Code != "" {
			apiErr.Code = parsed.Error.Code
			apiErr.Message = parsed.Error.Message
		}
		return apiErr
	}

	if out != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

// get/post/patch/delete shorthands used by the tool handlers.

func (c *Client) get(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, nil, out)
}

func (c *Client) post(ctx context.Context, path string, body, out any) error {
	return c.do(ctx, http.MethodPost, path, body, out)
}

func (c *Client) patch(ctx context.Context, path string, body, out any) error {
	return c.do(ctx, http.MethodPatch, path, body, out)
}

func (c *Client) deleteReq(ctx context.Context, path string, body, out any) error {
	return c.do(ctx, http.MethodDelete, path, body, out)
}

// --- small utilities used by tool handlers ---

// isThreeLetterKey returns true if s looks like a team or project key (AAA-ZZZ).
func isThreeLetterKey(s string) bool {
	if len(s) != 3 {
		return false
	}
	for i := 0; i < 3; i++ {
		if s[i] < 'A' || s[i] > 'Z' {
			return false
		}
	}
	return true
}

// resolveTeamID accepts either a ULID or a 3-letter team key and returns
// the team's ULID. If the input is already a ULID, it is returned unchanged.
func (c *Client) resolveTeamID(ctx context.Context, idOrKey string) (string, error) {
	if !isThreeLetterKey(idOrKey) {
		return idOrKey, nil
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	err := c.post(ctx, "/v1/teams/search", map[string]any{
		"where": map[string]any{"key": idOrKey},
		"limit": 1,
	}, &out)
	if err != nil {
		return "", fmt.Errorf("resolve team key %q: %w", idOrKey, err)
	}
	if len(out.Data) == 0 {
		return "", fmt.Errorf("no team found with key %q", idOrKey)
	}
	return out.Data[0].ID, nil
}

// resolveProjectID accepts a ULID or a 3-letter project key.
func (c *Client) resolveProjectID(ctx context.Context, idOrKey string) (string, error) {
	if !isThreeLetterKey(idOrKey) {
		return idOrKey, nil
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	err := c.post(ctx, "/v1/projects/search", map[string]any{
		"where": map[string]any{"key": idOrKey},
		"limit": 1,
	}, &out)
	if err != nil {
		return "", fmt.Errorf("resolve project key %q: %w", idOrKey, err)
	}
	if len(out.Data) == 0 {
		return "", fmt.Errorf("no project found with key %q", idOrKey)
	}
	return out.Data[0].ID, nil
}

// escapePathSeg URL-encodes a path segment so ticket display IDs like FOO-14
// survive being placed in a URL path (harmless for ULIDs and keys).
func escapePathSeg(s string) string {
	return url.PathEscape(s)
}
