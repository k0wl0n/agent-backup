package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/cenkalti/backoff/v5"
	"github.com/sony/gobreaker/v2"
)

type Client struct {
	BaseURL    string
	APIKey     string
	Hostname   string
	Type       string
	HTTPClient *http.Client
	AgentID    string
}

// ErrRevoked is returned by any client call that receives 401 or 403.
// The agent should treat this as a permanent termination signal.
var ErrRevoked = fmt.Errorf("agent revoked: API key rejected by server")

// controlPlaneBreaker guards all outbound calls to the backend control plane.
// Opens after 5 consecutive failures; probes again after 30 s (half-open).
var controlPlaneBreaker = gobreaker.NewCircuitBreaker[any](gobreaker.Settings{
	Name:        "control-plane",
	MaxRequests: 1,
	Interval:    60 * time.Second,
	Timeout:     30 * time.Second,
	ReadyToTrip: func(counts gobreaker.Counts) bool {
		return counts.ConsecutiveFailures >= 5
	},
})

// retryableCall wraps fn with exponential backoff (3 attempts) + circuit breaker.
// 401/403 responses are treated as permanent errors — they skip retry and open the breaker.
func retryableCall(ctx context.Context, fn func() error) error {
	_, err := backoff.Retry(ctx, func() (struct{}, error) {
		_, cbErr := controlPlaneBreaker.Execute(func() (any, error) {
			return nil, fn()
		})
		if cbErr == ErrRevoked {
			return struct{}{}, backoff.Permanent(cbErr)
		}
		return struct{}{}, cbErr
	}, backoff.WithMaxTries(3))
	return err
}

type RegisterRequest struct {
	APIKey   string `json:"api_key"`
	Hostname string `json:"hostname"`
	Version  string `json:"version"`
	Type     string `json:"type"`
}

type RegisterResponse struct {
	AgentID string `json:"agent_id"`
	Status  string `json:"status"`
}

type Task struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type TaskResult struct {
	TaskID     string `json:"task_id"`
	Result     string `json:"result,omitempty"`
	Error      string `json:"error,omitempty"`
	BackupSize int64  `json:"backup_size,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
}

func New(baseURL, apiKey, hostname, agentType string) *Client {
	return &Client{
		BaseURL:  baseURL,
		APIKey:   apiKey,
		Hostname: hostname,
		Type:     agentType,
		HTTPClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// setAgentHeaders adds both auth headers that the backend requires on all
// post-registration endpoints. Call this on every request after Register().
func (c *Client) setAgentHeaders(req *http.Request) {
	req.Header.Set("X-Agent-ID", c.AgentID)
	req.Header.Set("X-Agent-API-Key", c.APIKey)
}

func (c *Client) Register() error {
	hostname := c.Hostname
	if hostname == "" {
		hostname, _ = os.Hostname()
	}
	reqBody := RegisterRequest{
		APIKey:   c.APIKey,
		Hostname: hostname,
		Version:  "0.1.0",
		Type:     c.Type,
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}

	resp, err := c.HTTPClient.Post(c.BaseURL+"/api/v1/agent/register", "application/json", bytes.NewBuffer(jsonBody))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("registration failed: %s", resp.Status)
	}

	var registerResp RegisterResponse
	if err := json.NewDecoder(resp.Body).Decode(&registerResp); err != nil {
		return err
	}

	c.AgentID = registerResp.AgentID
	return nil
}

func (c *Client) SendHeartbeat() error {
	if c.AgentID == "" {
		return fmt.Errorf("agent not registered")
	}

	return retryableCall(context.Background(), func() error {
		req, err := http.NewRequest(http.MethodPost, c.BaseURL+"/api/v1/agent/heartbeat", nil)
		if err != nil {
			return err
		}

		c.setAgentHeaders(req)

		resp, err := c.HTTPClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return ErrRevoked
		}
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("heartbeat failed: %s", resp.Status)
		}
		return nil
	})
}

func (c *Client) PollTask() (*Task, error) {
	if c.AgentID == "" {
		return nil, fmt.Errorf("agent not registered")
	}

	var task *Task
	err := retryableCall(context.Background(), func() error {
		req, err := http.NewRequest(http.MethodGet, c.BaseURL+"/api/v1/agent/tasks", nil)
		if err != nil {
			return err
		}

		c.setAgentHeaders(req)

		resp, err := c.HTTPClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusNoContent {
			task = nil
			return nil
		}

		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return ErrRevoked
		}

		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("poll failed: %s", resp.Status)
		}

		var t Task
		if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
			return err
		}
		task = &t
		return nil
	})
	return task, err
}

func (c *Client) PushLog(taskID, level, component, message string) {
	if c.AgentID == "" {
		return
	}
	body, _ := json.Marshal(map[string]string{
		"task_id":   taskID,
		"level":     level,
		"component": component,
		"message":   message,
	})
	req, err := http.NewRequest(http.MethodPost, c.BaseURL+"/api/v1/agent/logs", bytes.NewBuffer(body))
	if err != nil {
		return
	}
	c.setAgentHeaders(req)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return
	}
	resp.Body.Close()
}

// GetEncryptionKey fetches the platform-derived AES encryption key for this agent.
// The backend derives the key from JOKOWIPE_SECRET_KEY + agent_id, so the backend
// can always re-derive it for transparent decryption on download.
func (c *Client) GetEncryptionKey() (string, error) {
	if c.AgentID == "" {
		return "", fmt.Errorf("agent not registered")
	}
	var key string
	err := retryableCall(context.Background(), func() error {
		req, err := http.NewRequest(http.MethodGet, c.BaseURL+"/api/v1/agent/encryption-key", nil)
		if err != nil {
			return err
		}
		c.setAgentHeaders(req)
		resp, err := c.HTTPClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusServiceUnavailable {
			// Server not configured with JOKOWIPE_SECRET_KEY — encryption unavailable
			key = ""
			return nil
		}
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return ErrRevoked
		}
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("get encryption key failed: %s", resp.Status)
		}
		var body struct {
			EncryptionKey string `json:"encryption_key"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			return err
		}
		key = body.EncryptionKey
		return nil
	})
	return key, err
}

// GetUploadURL fetches a fresh presigned PUT URL for the given task's backup artifact.
// Calling this just before uploading avoids clock-skew 403 ExpiredRequest errors from R2.
func (c *Client) GetUploadURL(taskID string) (string, error) {
	if c.AgentID == "" {
		return "", fmt.Errorf("agent not registered")
	}
	var uploadURL string
	err := retryableCall(context.Background(), func() error {
		req, err := http.NewRequest(http.MethodGet, c.BaseURL+"/api/v1/agent/tasks/"+taskID+"/upload-url", nil)
		if err != nil {
			return err
		}
		c.setAgentHeaders(req)
		resp, err := c.HTTPClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return ErrRevoked
		}
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("get upload URL failed: %s", resp.Status)
		}
		var body struct {
			UploadURL string `json:"upload_url"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			return err
		}
		uploadURL = body.UploadURL
		return nil
	})
	return uploadURL, err
}

func (c *Client) SubmitTaskResult(taskID string, result interface{}, errorMsg string, backupSize int64, durationMS int64) error {
	if c.AgentID == "" {
		return fmt.Errorf("agent not registered")
	}

	resultStr := ""
	if result != nil {
		if b, err := json.Marshal(result); err == nil {
			resultStr = string(b)
		}
	}

	reqBody := TaskResult{
		TaskID:     taskID,
		Result:     resultStr,
		Error:      errorMsg,
		BackupSize: backupSize,
		DurationMS: durationMS,
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}

	return retryableCall(context.Background(), func() error {
		req, err := http.NewRequest(http.MethodPost, c.BaseURL+"/api/v1/agent/tasks/submit", bytes.NewBuffer(jsonBody))
		if err != nil {
			return err
		}

		c.setAgentHeaders(req)
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.HTTPClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return ErrRevoked
		}
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("submit result failed: %s", resp.Status)
		}
		return nil
	})
}
