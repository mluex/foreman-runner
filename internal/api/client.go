// Package api is a thin client for the foreman server's runner endpoints.
package api

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client talks to a foreman server.
type Client struct {
	ServerURL string
	HTTP      *http.Client
}

// New returns a Client for serverURL. When insecure is set, TLS certificate
// verification is skipped (for local dev with self-signed certs only).
func New(serverURL string, insecure bool) *Client {
	transport := &http.Transport{}
	if insecure {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	return &Client{
		ServerURL: strings.TrimRight(serverURL, "/"),
		HTTP:      &http.Client{Timeout: 30 * time.Second, Transport: transport},
	}
}

// EnrollRequest is the body of POST /api/runners/enroll.
type EnrollRequest struct {
	Code         string `json:"code"`
	RunnerPubKey string `json:"runner_pubkey"` // base64, 32 bytes
	Hostname     string `json:"hostname"`
	OS           string `json:"os"`
	Arch         string `json:"arch"`
	NameHint     string `json:"name_hint,omitempty"`
}

// EnrollResponse is the server's reply to a successful enrollment.
type EnrollResponse struct {
	RunnerID   string `json:"runner_id"`
	APIToken   string `json:"api_token"`
	UserPubKey string `json:"user_pubkey"` // base64, 32 bytes
	ServerTime string `json:"server_time"`
}

// Enroll registers this runner with the server.
func (c *Client) Enroll(req EnrollRequest) (*EnrollResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}

	httpReq, err := http.NewRequest(http.MethodPost, c.ServerURL+"/api/runners/enroll", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("enroll request: %w", err)
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server rejected enrollment (%s): %s", resp.Status, serverError(data))
	}

	var out EnrollResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &out, nil
}

// serverError extracts the "error" field from a JSON error body, falling back
// to the raw payload.
func serverError(data []byte) string {
	var e struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(data, &e); err == nil && e.Error != "" {
		return e.Error
	}
	return strings.TrimSpace(string(data))
}
