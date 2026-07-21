package hangar

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClient_GetMCPServerTools_Success(t *testing.T) {
	// Setup mock server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/mcp_servers/test-provider/tools", r.URL.Path)
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "test-api-key", r.Header.Get("X-API-Key"))

		response := map[string]interface{}{
			"tools": []string{"tool1", "tool2", "tool3"},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	client := NewClient(&Config{
		URL:    server.URL,
		APIKey: "test-api-key",
	})

	// Execute
	tools, err := client.GetMCPServerTools(context.Background(), "test-provider", "default")

	// Assert
	require.NoError(t, err)
	assert.Len(t, tools, 3)
	assert.Contains(t, tools, "tool1")
	assert.Contains(t, tools, "tool2")
	assert.Contains(t, tools, "tool3")
}

func TestClient_GetMCPServerTools_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": "provider not found",
		})
	}))
	defer server.Close()

	client := NewClient(&Config{
		URL:    server.URL,
		APIKey: "test-api-key",
	})

	tools, err := client.GetMCPServerTools(context.Background(), "nonexistent", "default")

	assert.Error(t, err)
	assert.Nil(t, tools)
	assert.Contains(t, err.Error(), "provider not found")
}

func TestClient_GetMCPServerTools_Timeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
	}))
	defer server.Close()

	client := NewClient(&Config{
		URL:     server.URL,
		APIKey:  "test-api-key",
		Timeout: 10 * time.Millisecond,
	})

	ctx := context.Background()
	tools, err := client.GetMCPServerTools(ctx, "test-provider", "default")

	assert.Error(t, err)
	assert.Nil(t, tools)
}

func TestClient_HealthCheckRemote_Healthy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/health/remote", r.URL.Path)
		assert.Equal(t, "POST", r.Method)

		response := map[string]interface{}{
			"healthy": true,
			"tools":   []string{"remote-tool1", "remote-tool2"},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	client := NewClient(&Config{
		URL:    server.URL,
		APIKey: "test-api-key",
	})

	healthy, tools, err := client.HealthCheckRemote(context.Background(), "https://api.example.com")

	require.NoError(t, err)
	assert.True(t, healthy)
	assert.Len(t, tools, 2)
	assert.Contains(t, tools, "remote-tool1")
}

func TestClient_HealthCheckRemote_Unhealthy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := map[string]interface{}{
			"healthy": false,
			"tools":   []string{},
			"error":   "connection refused",
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	client := NewClient(&Config{
		URL:    server.URL,
		APIKey: "test-api-key",
	})

	healthy, tools, err := client.HealthCheckRemote(context.Background(), "https://broken.example.com")

	assert.Error(t, err)
	assert.False(t, healthy)
	assert.Empty(t, tools)
	assert.Contains(t, err.Error(), "connection refused")
}

func TestClient_RegisterProvider_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/providers", r.URL.Path)
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		var body RegisterMCPServerRequest
		err := json.NewDecoder(r.Body).Decode(&body)
		require.NoError(t, err)

		assert.Equal(t, "test-provider", body.Name)
		assert.Equal(t, "default", body.Namespace)

		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status": "registered",
		})
	}))
	defer server.Close()

	client := NewClient(&Config{
		URL:    server.URL,
		APIKey: "test-api-key",
	})

	req := &RegisterMCPServerRequest{
		Name:      "test-provider",
		Namespace: "default",
		Mode:      "container",
		Image:     "test:latest",
	}

	err := client.RegisterMCPServer(context.Background(), req)

	assert.NoError(t, err)
}

func TestClient_DeregisterProvider_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/mcp_servers/test-provider", r.URL.Path)
		assert.Equal(t, "DELETE", r.Method)
		assert.Equal(t, "test-api-key", r.Header.Get("X-API-Key"))

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status": "deregistered",
		})
	}))
	defer server.Close()

	client := NewClient(&Config{
		URL:    server.URL,
		APIKey: "test-api-key",
	})

	err := client.DeregisterMCPServer(context.Background(), "test-provider", "default")

	assert.NoError(t, err)
}

func TestClient_DeregisterProvider_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	client := NewClient(&Config{
		URL:    server.URL,
		APIKey: "test-api-key",
	})

	// Should not error on 404 - provider already gone
	err := client.DeregisterMCPServer(context.Background(), "nonexistent", "default")

	assert.NoError(t, err)
}

func TestClient_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": "internal server error",
		})
	}))
	defer server.Close()

	client := NewClient(&Config{
		URL:    server.URL,
		APIKey: "test-api-key",
	})

	tools, err := client.GetMCPServerTools(context.Background(), "test-provider", "default")

	assert.Error(t, err)
	assert.Nil(t, tools)
	assert.Contains(t, err.Error(), "500")
}

func TestClient_InvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("invalid json {"))
	}))
	defer server.Close()

	client := NewClient(&Config{
		URL:    server.URL,
		APIKey: "test-api-key",
	})

	tools, err := client.GetMCPServerTools(context.Background(), "test-provider", "default")

	assert.Error(t, err)
	assert.Nil(t, tools)
}

func TestClient_ContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
	}))
	defer server.Close()

	client := NewClient(&Config{
		URL:    server.URL,
		APIKey: "test-api-key",
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	tools, err := client.GetMCPServerTools(ctx, "test-provider", "default")

	assert.Error(t, err)
	assert.Nil(t, tools)
	assert.Contains(t, err.Error(), "context canceled")
}

func TestNewClient(t *testing.T) {
	client := NewClient(&Config{
		URL:    "http://localhost:8080",
		APIKey: "my-api-key",
	})

	assert.NotNil(t, client)
	assert.Equal(t, "http://localhost:8080", client.baseURL)
	assert.Equal(t, "my-api-key", client.apiKey)
	assert.NotNil(t, client.httpClient)
	assert.Equal(t, 30*time.Second, client.httpClient.Timeout)
	assert.Equal(t, 3, client.maxRetries)
	assert.Equal(t, 500*time.Millisecond, client.baseDelay)
}

func TestNewClient_CustomRetryConfig(t *testing.T) {
	client := NewClient(&Config{
		URL:        "http://localhost:8080",
		APIKey:     "key",
		MaxRetries: 5,
		BaseDelay:  1 * time.Second,
	})

	assert.Equal(t, 5, client.maxRetries)
	assert.Equal(t, 1*time.Second, client.baseDelay)
}

func TestClient_Retry_On5xx(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("temporarily unavailable"))
			return
		}
		// Third attempt succeeds
		response := map[string]interface{}{
			"tools": []string{"tool1"},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	client := NewClient(&Config{
		URL:        server.URL,
		APIKey:     "test-api-key",
		MaxRetries: 3,
		BaseDelay:  10 * time.Millisecond, // Fast retries for test
	})

	tools, err := client.GetMCPServerTools(context.Background(), "test-provider", "default")

	require.NoError(t, err)
	assert.Len(t, tools, 1)
	assert.Equal(t, 3, attempts, "should have retried twice before succeeding")
}

func TestClient_Retry_ExhaustedReturnsError(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("server error"))
	}))
	defer server.Close()

	client := NewClient(&Config{
		URL:        server.URL,
		APIKey:     "test-api-key",
		MaxRetries: 2,
		BaseDelay:  10 * time.Millisecond,
	})

	tools, err := client.GetMCPServerTools(context.Background(), "test-provider", "default")

	assert.Error(t, err)
	assert.Nil(t, tools)
	assert.Contains(t, err.Error(), "failed after 2 retries")
	assert.Equal(t, 3, attempts, "should have tried 1 + 2 retries = 3 total")
}

func TestClient_Retry_No4xxRetry(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
	}))
	defer server.Close()

	client := NewClient(&Config{
		URL:        server.URL,
		APIKey:     "test-api-key",
		MaxRetries: 3,
		BaseDelay:  10 * time.Millisecond,
	})

	tools, err := client.GetMCPServerTools(context.Background(), "nonexistent", "default")

	assert.Error(t, err)
	assert.Nil(t, tools)
	assert.Equal(t, 1, attempts, "should NOT retry on 4xx errors")
}
