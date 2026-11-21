// Package solver calls the solver service.
package solver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client posts re-plans to the solver service.
type Client struct {
	base string
	http *http.Client
}

// New returns a client for the solver at baseURL (e.g. http://localhost:8081).
func New(baseURL string, timeout time.Duration) *Client {
	return &Client{base: strings.TrimRight(baseURL, "/"), http: &http.Client{Timeout: timeout}}
}

// Replan asks the solver to re-plan and commit a facility now. It returns the solver's JSON answer and status.
func (c *Client) Replan(ctx context.Context, facilityID, detail string) (json.RawMessage, int, error) {
	body, _ := json.Marshal(map[string]string{"facilityId": facilityID, "trigger": "MANUAL", "detail": detail})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/replan", bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("solver: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("solver: %w", err)
	}
	if !json.Valid(raw) {
		return nil, resp.StatusCode, fmt.Errorf("solver answered %d with a non-JSON body", resp.StatusCode)
	}
	return raw, resp.StatusCode, nil
}
