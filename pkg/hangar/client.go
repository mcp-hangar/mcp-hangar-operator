// Package hangar provides a client for communicating with MCP-Hangar core
package hangar

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/mcp-hangar/operator/pkg/metrics"
)

// observe returns a deferred recorder for a Hangar client call: it records the
// call latency and, if the pointed-to error is non-nil, increments the error
// counter. Usage (the method must have a named error return):
//
//	func (c *Client) Foo(...) (err error) {
//	    defer c.observe("foo")(&err)
//	    ...
//	}
func (c *Client) observe(operation string) func(*error) {
	start := time.Now()
	return func(errp *error) {
		metrics.HangarClientLatency.WithLabelValues(operation).Observe(time.Since(start).Seconds())
		if errp != nil && *errp != nil {
			metrics.HangarClientErrors.WithLabelValues(operation).Inc()
		}
	}
}

// Client communicates with MCP-Hangar core
type Client struct {
	baseURL    string
	httpClient *http.Client
	apiKey     string
	maxRetries int
	baseDelay  time.Duration
}

// Config holds client configuration
type Config struct {
	// URL of MCP-Hangar core service
	URL string

	// APIKey for authentication
	APIKey string

	// Timeout for requests
	Timeout time.Duration

	// MaxRetries is the maximum number of retries on transient errors (default: 3)
	MaxRetries int

	// BaseDelay is the initial delay between retries (default: 500ms, doubles each retry)
	BaseDelay time.Duration
}

// DefaultConfig returns default client configuration
func DefaultConfig() *Config {
	return &Config{
		URL:        "http://mcp-hangar.mcp-hangar.svc.cluster.local:8080",
		Timeout:    30 * time.Second,
		MaxRetries: 3,
		BaseDelay:  500 * time.Millisecond,
	}
}

// NewClient creates a new Hangar client
func NewClient(config *Config) *Client {
	if config == nil {
		config = DefaultConfig()
	}

	timeout := config.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	maxRetries := config.MaxRetries
	if maxRetries <= 0 {
		maxRetries = 3
	}

	baseDelay := config.BaseDelay
	if baseDelay <= 0 {
		baseDelay = 500 * time.Millisecond
	}

	return &Client{
		baseURL: config.URL,
		apiKey:  config.APIKey,
		httpClient: &http.Client{
			Timeout: timeout,
		},
		maxRetries: maxRetries,
		baseDelay:  baseDelay,
	}
}

// doWithRetry executes an HTTP request with exponential backoff retry on
// transient failures (network errors and 5xx responses). Non-retryable
// responses (2xx, 4xx) are returned immediately.
func (c *Client) doWithRetry(ctx context.Context, method, url string, body []byte) (*http.Response, error) {
	var lastErr error

	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if attempt > 0 {
			delay := c.baseDelay * time.Duration(1<<(attempt-1))
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}

		var bodyReader io.Reader
		if body != nil {
			bodyReader = bytes.NewReader(body)
		}

		req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}

		c.setHeaders(req)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("request failed: %w", err)
			continue
		}

		// Retry on 5xx server errors
		if resp.StatusCode >= 500 {
			respBody, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			lastErr = fmt.Errorf("server error %d: %s", resp.StatusCode, string(respBody))
			continue
		}

		return resp, nil
	}

	return nil, fmt.Errorf("request failed after %d retries: %w", c.maxRetries, lastErr)
}

// MCPServerInfo represents provider information from Hangar
type MCPServerInfo struct {
	Name       string   `json:"name"`
	Namespace  string   `json:"namespace"`
	State      string   `json:"state"`
	Tools      []string `json:"tools"`
	ToolsCount int      `json:"tools_count"`
	Endpoint   string   `json:"endpoint,omitempty"`
}

// ToolInfo represents tool information
type ToolInfo struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	InputSchema map[string]interface{} `json:"input_schema,omitempty"`
}

// GetMCPServerTools fetches the list of tools from a provider
func (c *Client) GetMCPServerTools(ctx context.Context, name, namespace string) (tools []string, err error) {
	defer c.observe("get_tools")(&err)
	url := fmt.Sprintf("%s/api/v1/providers/%s/%s/tools", c.baseURL, namespace, name)

	resp, err := c.doWithRetry(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("provider not found: %s/%s", namespace, name)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Tools []string `json:"tools"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return result.Tools, nil
}

// GetProvider fetches provider information
func (c *Client) GetMCPServer(ctx context.Context, name, namespace string) (_ *MCPServerInfo, err error) {
	defer c.observe("get_server")(&err)
	url := fmt.Sprintf("%s/api/v1/providers/%s/%s", c.baseURL, namespace, name)

	resp, err := c.doWithRetry(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil // Provider doesn't exist
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}

	var info MCPServerInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &info, nil
}

// HealthCheckRemote checks if a remote endpoint is healthy
func (c *Client) HealthCheckRemote(ctx context.Context, endpoint string) (healthy bool, tools []string, err error) {
	defer c.observe("health_check")(&err)
	url := fmt.Sprintf("%s/api/v1/health/remote", c.baseURL)

	payload := struct {
		Endpoint string `json:"endpoint"`
	}{
		Endpoint: endpoint,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return false, nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	resp, err := c.doWithRetry(ctx, http.MethodPost, url, body)
	if err != nil {
		return false, nil, err
	}
	defer resp.Body.Close()

	var result struct {
		Healthy bool     `json:"healthy"`
		Tools   []string `json:"tools"`
		Error   string   `json:"error,omitempty"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if result.Error != "" {
		return false, nil, fmt.Errorf("health check error: %s", result.Error)
	}

	return result.Healthy, result.Tools, nil
}

// RegisterProvider registers a provider with Hangar core
type RegisterMCPServerRequest struct {
	Name      string            `json:"name"`
	Namespace string            `json:"namespace"`
	Mode      string            `json:"mode"`
	Endpoint  string            `json:"endpoint,omitempty"`
	Image     string            `json:"image,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
}

func (c *Client) RegisterMCPServer(ctx context.Context, req *RegisterMCPServerRequest) (err error) {
	defer c.observe("register")(&err)
	url := fmt.Sprintf("%s/api/v1/providers", c.baseURL)

	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	c.setHeaders(httpReq)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("registration failed with status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// L7ToolRules mirrors the L7 tool-glob rules the core parses.
type L7ToolRules struct {
	Allow           []string `json:"allow,omitempty"`
	Deny            []string `json:"deny,omitempty"`
	RequireApproval []string `json:"requireApproval,omitempty"`
}

// L7ArgumentRules mirrors the L7 deterministic argument constraints.
type L7ArgumentRules struct {
	SecretPatterns  []string `json:"secretPatterns,omitempty"`
	MaxPayloadBytes *int64   `json:"maxPayloadBytes,omitempty"`
}

// L7PolicyPayload is the compiled L7 egress policy the operator pushes to core.
// It matches the wire form core's L7Policy.from_dict parses.
type L7PolicyPayload struct {
	Tools         L7ToolRules     `json:"tools"`
	Arguments     L7ArgumentRules `json:"arguments"`
	DefaultAction string          `json:"defaultAction"`
}

// SetL7Policy pushes a compiled L7 egress policy for an mcp_server to core.
func (c *Client) SetL7Policy(ctx context.Context, mcpServerID string, policy *L7PolicyPayload) (err error) {
	defer c.observe("set_l7_policy")(&err)
	url := fmt.Sprintf("%s/api/mcp_servers/%s/l7_policy", c.baseURL, mcpServerID)

	body, err := json.Marshal(policy)
	if err != nil {
		return fmt.Errorf("failed to marshal L7 policy: %w", err)
	}

	resp, err := c.doWithRetry(ctx, http.MethodPost, url, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("set L7 policy failed with status %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// ClearL7Policy removes the L7 egress policy for an mcp_server. A 404 is treated
// as success (the server is already gone).
func (c *Client) ClearL7Policy(ctx context.Context, mcpServerID string) (err error) {
	defer c.observe("clear_l7_policy")(&err)
	url := fmt.Sprintf("%s/api/mcp_servers/%s/l7_policy", c.baseURL, mcpServerID)

	resp, err := c.doWithRetry(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("clear L7 policy failed with status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// DeregisterProvider removes a provider from Hangar core
func (c *Client) DeregisterMCPServer(ctx context.Context, name, namespace string) (err error) {
	defer c.observe("deregister")(&err)
	url := fmt.Sprintf("%s/api/v1/providers/%s/%s", c.baseURL, namespace, name)

	resp, err := c.doWithRetry(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// 404 is OK - provider already gone
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("deregistration failed with status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// StartProvider starts a cold provider
func (c *Client) StartMCPServer(ctx context.Context, name, namespace string) (err error) {
	defer c.observe("start")(&err)
	url := fmt.Sprintf("%s/api/v1/providers/%s/%s/start", c.baseURL, namespace, name)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("start failed with status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// StopProvider stops a provider
func (c *Client) StopMCPServer(ctx context.Context, name, namespace string) (err error) {
	defer c.observe("stop")(&err)
	url := fmt.Sprintf("%s/api/v1/providers/%s/%s/stop", c.baseURL, namespace, name)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("stop failed with status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// Ping checks connectivity to Hangar core
func (c *Client) Ping(ctx context.Context) (err error) {
	defer c.observe("ping")(&err)
	url := fmt.Sprintf("%s/health", c.baseURL)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("ping failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unhealthy: status %d", resp.StatusCode)
	}

	return nil
}

// setHeaders sets common headers for all requests
func (c *Client) setHeaders(req *http.Request) {
	if c.apiKey != "" {
		req.Header.Set("X-API-Key", c.apiKey)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "mcp-hangar-operator/1.0")
}
