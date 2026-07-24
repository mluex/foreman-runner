// Package api is a thin client for the foreman server's runner endpoints.
package api

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
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
	Code            string `json:"code"`
	RunnerPubKey    string `json:"runner_pubkey"`               // base64, 32 bytes (Ed25519)
	RunnerEncPubKey string `json:"runner_enc_pubkey,omitempty"` // base64, 32 bytes (X25519)
	Hostname        string `json:"hostname"`
	OS              string `json:"os"`
	Arch            string `json:"arch"`
	NameHint        string `json:"name_hint,omitempty"`
}

// EnrollResponse is the server's reply to a successful enrollment.
type EnrollResponse struct {
	RunnerID      string `json:"runner_id"`
	APIToken      string `json:"api_token"`
	UserPubKey    string `json:"user_pubkey"`     // base64, 32 bytes (Ed25519)
	UserEncPubKey string `json:"user_enc_pubkey"` // base64, 32 bytes (X25519)
	ServerTime    string `json:"server_time"`
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

// Agent is a coding agent the runner reports as available.
type Agent struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Path    string `json:"path"`
	AuthOK  bool   `json:"auth_ok"`
}

// System carries lightweight host metrics.
type System struct {
	Load1     float64 `json:"load1"`
	MemFreeMB int64   `json:"mem_free_mb"`
}

// HeartbeatRequest is the body of POST /api/runners/heartbeat.
type HeartbeatRequest struct {
	RunnerID  string  `json:"runner_id"`
	Timestamp string  `json:"timestamp"`
	Nonce     string  `json:"nonce"`
	EncPubKey string  `json:"enc_pubkey,omitempty"` // base64 X25519 public key, so the server can pick up runners enrolled before E2E
	Agents    []Agent `json:"agents"`
	System    System  `json:"system"`
}

// HeartbeatResponse is the server's reply; UserPubKey lets the runner pick up
// owner key rotations.
type HeartbeatResponse struct {
	UserPubKey    string `json:"user_pubkey"`
	UserEncPubKey string `json:"user_enc_pubkey"`
	ServerTime    string `json:"server_time"`
}

// Heartbeat sends a signed heartbeat. The body is signed with the runner's
// Ed25519 private key and the exact signed bytes are sent verbatim.
func (c *Client) Heartbeat(token string, privKey ed25519.PrivateKey, req HeartbeatRequest) (*HeartbeatResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}

	httpReq, err := http.NewRequest(http.MethodPost, c.ServerURL+"/api/runners/heartbeat", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("X-Signature", base64.StdEncoding.EncodeToString(ed25519.Sign(privKey, body)))

	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("heartbeat request: %w", err)
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("heartbeat rejected (%s): %s", resp.Status, serverError(data))
	}

	var out HeartbeatResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &out, nil
}

// NextTaskResponse is a claimed task: the exact signed payload bytes and the
// detached signature to verify against the owner's public key.
type NextTaskResponse struct {
	TaskID    string `json:"task_id"`
	Payload   string `json:"payload"`
	Signature string `json:"signature"`
}

// TaskPayload is the parsed canonical task payload. Optional fields are empty
// strings when the payload carried null.
type TaskPayload struct {
	TaskID   string `json:"task_id"`
	RunnerID string `json:"runner_id"`
	Agent    string `json:"agent"`
	Model    string `json:"model"`
	Mode     string `json:"mode"`
	Prompt   string `json:"prompt"`
	Title    string `json:"title"`
	Enc      string `json:"enc"` // "" or "x25519-sealedbox"; when set, prompt/title are base64 sealed boxes
}

// NextTask claims the next pending task for the runner, or returns (nil, nil)
// when the queue is empty.
func (c *Client) NextTask(runnerID, token string) (*NextTaskResponse, error) {
	httpReq, err := http.NewRequest(http.MethodGet, c.ServerURL+"/api/runners/"+runnerID+"/next-task", nil)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("next-task request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}

	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("next-task failed (%s): %s", resp.Status, serverError(data))
	}

	var out NextTaskResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &out, nil
}

// TaskStatusResponse reports a task's current server-side status and whether
// the operator has requested its cancellation.
type TaskStatusResponse struct {
	Status          string `json:"status"`
	CancelRequested bool   `json:"cancel_requested"`
}

// TaskStatus fetches the current status of a task. It is a lightweight
// bearer-authenticated poll (no signed body) the runner uses to notice a
// cancellation requested from the web UI while a task is running.
func (c *Client) TaskStatus(taskID, token string) (*TaskStatusResponse, error) {
	httpReq, err := http.NewRequest(http.MethodGet, c.ServerURL+"/api/tasks/"+taskID+"/status", nil)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("task-status request: %w", err)
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("task-status failed (%s): %s", resp.Status, serverError(data))
	}

	var out TaskStatusResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &out, nil
}

// LatestVersion returns the tag name of the latest published runner release,
// as reported by the runner's own server.
func (c *Client) LatestVersion(token string) (string, error) {
	httpReq, err := http.NewRequest(http.MethodGet, c.ServerURL+"/api/runners/latest-version", nil)
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("latest-version request: %w", err)
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("latest-version failed (%s): %s", resp.Status, serverError(data))
	}

	var out struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	return out.Version, nil
}

// DownloadAsset streams a release asset through the server's /dl proxy, which
// redirects to the matching asset of the latest release. The caller must close
// the returned body. A dedicated client with a generous timeout is used so a
// multi-megabyte binary is not cut off by the short API timeout.
func (c *Client) DownloadAsset(asset string) (io.ReadCloser, error) {
	httpReq, err := http.NewRequest(http.MethodGet, c.ServerURL+"/dl/"+asset, nil)
	if err != nil {
		return nil, err
	}

	dl := &http.Client{Timeout: 5 * time.Minute, Transport: c.HTTP.Transport}
	resp, err := dl.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", asset, err)
	}
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		return nil, fmt.Errorf("download %s failed (%s): %s", asset, resp.Status, serverError(data))
	}
	return resp.Body, nil
}

// RejectTask marks a claimed task rejected (e.g. after a failed signature check).
func (c *Client) RejectTask(taskID, token string, privKey ed25519.PrivateKey, reason string) error {
	return c.postSigned(c.ServerURL+"/api/tasks/"+taskID+"/reject", token, privKey, map[string]any{
		"reason":    reason,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"nonce":     newNonce(),
	})
}

// FinishTask reports the agent's exit code for a running task.
func (c *Client) FinishTask(taskID, token string, privKey ed25519.PrivateKey, exitCode int) error {
	return c.postSigned(c.ServerURL+"/api/tasks/"+taskID+"/finish", token, privKey, map[string]any{
		"exit_code": exitCode,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"nonce":     newNonce(),
	})
}

// SendLog appends a captured log chunk for a task. Chunks are keyed by a
// per-task monotonic seq; the server dedups on (task, seq), so resending a seq
// is idempotent.
func (c *Client) SendLog(taskID, token string, privKey ed25519.PrivateKey, seq int, chunk string) error {
	return c.postSigned(c.ServerURL+"/api/tasks/"+taskID+"/logs", token, privKey, map[string]any{
		"seq":       seq,
		"chunk":     chunk,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"nonce":     newNonce(),
	})
}

func (c *Client) postSigned(url, token string, privKey ed25519.PrivateKey, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode request: %w", err)
	}

	httpReq, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("X-Signature", base64.StdEncoding.EncodeToString(ed25519.Sign(privKey, body)))

	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return fmt.Errorf("request %s: %w", url, err)
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("request rejected (%s): %s", resp.Status, serverError(data))
	}
	return nil
}

func newNonce() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.StdEncoding.EncodeToString(b)
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
