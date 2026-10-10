package hangar

import (
	"context"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A wrong API key came back as a generic "unexpected status" error, which the
// controller could not tell from an outage (#212).
func TestClient_AuthRejectionIsTyped(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(code)
		}))
		c := NewClient(&Config{URL: srv.URL, MaxRetries: 1})

		_, err := c.GetMCPServerHealth(context.Background(), "srv", "default")
		assert.True(t, IsAuthRejected(err), "health %d: %v", code, err)
		_, err = c.GetMCPServerTools(context.Background(), "srv", "default")
		assert.True(t, IsAuthRejected(err), "tools %d: %v", code, err)
		srv.Close()
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()
	_, err := NewClient(&Config{URL: srv.URL, MaxRetries: 1}).GetMCPServerHealth(context.Background(), "srv", "default")
	require.Error(t, err)
	assert.False(t, IsAuthRejected(err), "a 400 is not an auth rejection")
}

// An https core signed by a private CA was unreachable: the client used the
// system roots only (#212).
func TestClient_PrivateCAFromFile(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"consecutive_failures": 0}`))
	}))
	defer srv.Close()

	_, err := NewClient(&Config{URL: srv.URL, MaxRetries: 1}).GetMCPServerHealth(context.Background(), "srv", "default")
	require.Error(t, err, "without the CA the certificate must not verify")

	caFile := filepath.Join(t.TempDir(), "ca.pem")
	require.NoError(t, os.WriteFile(caFile,
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600))
	tlsConfig, err := TLSConfigFromCAFile(caFile, "")
	require.NoError(t, err)

	_, err = NewClient(&Config{URL: srv.URL, MaxRetries: 1, TLSConfig: tlsConfig}).
		GetMCPServerHealth(context.Background(), "srv", "default")
	assert.NoError(t, err)
}

func TestTLSConfigFromCAFile_RejectsANonPEMFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "ca.pem")
	require.NoError(t, os.WriteFile(f, []byte("not a certificate"), 0o600))
	_, err := TLSConfigFromCAFile(f, "")
	assert.Error(t, err)
}
