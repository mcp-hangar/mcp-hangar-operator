package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())
	return addr
}

// Served plain, /metrics told any pod in the cluster the operator's server
// names, states, tool counts and reconcile errors (#193). By default it is now
// HTTPS, and a request without a token the API server accepts is refused.
func TestMetrics_SecureByDefault_RefusesAnonymousScrape(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("needs envtest binaries (KUBEBUILDER_ASSETS)")
	}
	env := &envtest.Environment{}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { _ = env.Stop() })

	addr := freeAddr(t)
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsServerOptions(addr, true),
		HealthProbeBindAddress: "0",
	})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = mgr.Start(ctx) }()

	insecureTLS := &http.Client{
		Timeout:   2 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, //nolint:gosec // self-signed by design
	}
	status := 0
	require.Eventually(t, func() bool {
		resp, err := insecureTLS.Get(fmt.Sprintf("https://%s/metrics", addr))
		if err != nil {
			return false
		}
		status = resp.StatusCode
		_ = resp.Body.Close()
		return true
	}, 15*time.Second, 100*time.Millisecond, "the metrics endpoint should serve HTTPS")
	assert.Equal(t, http.StatusUnauthorized, status, "an anonymous scrape must be refused")

	plain := &http.Client{Timeout: 2 * time.Second}
	if r, err := plain.Get(fmt.Sprintf("http://%s/metrics", addr)); err == nil {
		_ = r.Body.Close()
		assert.NotEqual(t, http.StatusOK, r.StatusCode, "plain HTTP must not serve metrics")
	}
}

func TestMetrics_InsecureOptOutKeepsPlainHTTP(t *testing.T) {
	opts := metricsServerOptions(":8080", false)
	assert.False(t, opts.SecureServing)
	assert.Nil(t, opts.FilterProvider)
}

// Chart 0.12.38 and 0.12.39 pass both retired flags; #312 made them no-ops,
// so the operator must still parse them, and say they do nothing.
func TestRetiredFlags_ParseAndSayTheyDoNothing(t *testing.T) {
	fs := flag.NewFlagSet("operator", flag.ContinueOnError)
	registerRetiredFlags(fs)
	require.NoError(t, fs.Parse([]string{"--image-digest-policy=block", "--webhook-annotation-rules=false"}))
	notes := retiredFlagNotes(fs)
	require.Len(t, notes, 2)
	assert.Contains(t, notes[0], "--image-digest-policy=block has no effect")
	assert.Contains(t, notes[1], "--webhook-annotation-rules=false has no effect")

	fs = flag.NewFlagSet("operator", flag.ContinueOnError)
	registerRetiredFlags(fs)
	require.NoError(t, fs.Parse([]string{"--webhook-annotation-rules"}))
	notes = retiredFlagNotes(fs)
	require.Len(t, notes, 1)
	assert.Contains(t, notes[0], "nothing does", "asking for the webhook rules must hear they are gone")

	fs = flag.NewFlagSet("operator", flag.ContinueOnError)
	registerRetiredFlags(fs)
	require.NoError(t, fs.Parse(nil))
	assert.Empty(t, retiredFlagNotes(fs), "a manifest without the flags hears nothing")
}
