// Package conversion_test exercises a real v1alpha1<->v1alpha2 conversion
// round-trip through the conversion webhook served by the manager, using
// envtest. This is the end-to-end counterpart to the unit-level ConvertTo /
// ConvertFrom tests in api/v1alpha1: it proves that with the conversion webhook
// wired (issue #11), the apiserver actually routes conversion through the
// operator's hand-written logic instead of blindly relabelling apiVersion.
package conversion_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/conversion"
	"sigs.k8s.io/yaml"

	mcpv1alpha1 "github.com/mcp-hangar/operator/api/v1alpha1"
	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
)

func TestConversionWebhookRoundTrip(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" && firstEnvTestBinDir() == "" {
		t.Skip("KUBEBUILDER_ASSETS not set and no bin/k8s assets found; skipping envtest conversion round-trip")
	}

	scheme := runtime.NewScheme()
	utilruntime.Must(mcpv1alpha1.AddToScheme(scheme))
	utilruntime.Must(mcpv1alpha2.AddToScheme(scheme))

	crds, err := loadCRDs()
	require.NoError(t, err)

	testEnv := &envtest.Environment{
		Scheme:                scheme,
		CRDs:                  crds,
		ErrorIfCRDPathMissing: true,
		// An empty WebhookInstallOptions still provisions serving certs and a
		// local serving host/port; envtest rewrites every convertible CRD's
		// conversion webhook clientConfig to point at that local server.
		WebhookInstallOptions: envtest.WebhookInstallOptions{},
	}
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		testEnv.BinaryAssetsDirectory = firstEnvTestBinDir()
	}

	cfg, err := testEnv.Start()
	require.NoError(t, err)
	require.NotNil(t, cfg)
	defer func() { _ = testEnv.Stop() }()

	wo := testEnv.WebhookInstallOptions
	webhookServer := webhook.NewServer(webhook.Options{
		Host:    wo.LocalServingHost,
		Port:    wo.LocalServingPort,
		CertDir: wo.LocalServingCertDir,
	})

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:         scheme,
		Metrics:        metricsserver.Options{BindAddress: "0"},
		WebhookServer:  webhookServer,
		LeaderElection: false,
	})
	require.NoError(t, err)

	// Serve the conversion webhook. This is the same "/convert" handler that
	// controller-runtime installs when the operator registers a webhook builder
	// for a convertible type in cmd/operator/main.go.
	mgr.GetWebhookServer().Register("/convert", conversion.NewWebhookHandler(scheme))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = mgr.Start(ctx) }()

	// Wait for the webhook server to come up.
	checker := mgr.GetWebhookServer().StartedChecker()
	require.Eventually(t, func() bool { return checker(nil) == nil }, 30*time.Second, 200*time.Millisecond)

	k8sClient, err := client.New(cfg, client.Options{Scheme: scheme})
	require.NoError(t, err)

	name := "conv-roundtrip"
	ns := "default"

	// Create the object as v1alpha1 (a served, non-storage version). Persisting
	// it forces a v1alpha1 -> v1alpha2 (storage) conversion through the webhook.
	v1 := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: mcpv1alpha1.MCPServerSpec{
			Mode:     mcpv1alpha1.MCPServerModeRemote,
			Endpoint: "https://api.example.com/mcp",
			IdleTTL:  "5m",
		},
	}
	require.Eventually(t, func() bool {
		return k8sClient.Create(ctx, v1) == nil
	}, 30*time.Second, 500*time.Millisecond, "create via v1alpha1 should succeed once the conversion webhook is reachable")

	// Read it back as v1alpha2 (storage): this is a storage->v1alpha2 no-op read,
	// but confirms the object round-tripped through conversion on write.
	gotV2 := &mcpv1alpha2.MCPServer{}
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKey{Name: name, Namespace: ns}, gotV2))
	assert.Equal(t, mcpv1alpha2.MCPServerModeRemote, gotV2.Spec.Mode)
	assert.Equal(t, "https://api.example.com/mcp", gotV2.Spec.Endpoint)
	require.NotNil(t, gotV2.Spec.IdleTTL, "idleTTL must survive conversion into the typed v1alpha2 metav1.Duration")
	assert.Equal(t, 5*time.Minute, gotV2.Spec.IdleTTL.Duration)

	// And read it back as v1alpha1 (storage v1alpha2 -> v1alpha1): a second
	// conversion in the reverse direction.
	gotV1 := &mcpv1alpha1.MCPServer{}
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKey{Name: name, Namespace: ns}, gotV1))
	assert.Equal(t, mcpv1alpha1.MCPServerModeRemote, gotV1.Spec.Mode)
	// The value round-tripped through the typed v1alpha2 metav1.Duration and back
	// to the v1alpha1 string form, so it is normalized ("5m" -> "5m0s"). This
	// normalization is itself proof the hand-written conversion ran rather than a
	// blind apiVersion relabel.
	assert.Equal(t, "5m0s", gotV1.Spec.IdleTTL, "idleTTL must convert back to the v1alpha1 string form")
}

func loadCRDs() ([]*apiextensionsv1.CustomResourceDefinition, error) {
	base := filepath.Join("..", "..", "config", "crd", "bases")
	files := []string{
		"mcp-hangar.io_mcpservers.yaml",
		"mcp-hangar.io_mcpservergroups.yaml",
		"mcp-hangar.io_mcpdiscoverysources.yaml",
	}
	crds := make([]*apiextensionsv1.CustomResourceDefinition, 0, len(files))
	for _, name := range files {
		data, err := os.ReadFile(filepath.Join(base, name))
		if err != nil {
			return nil, err
		}
		crd := &apiextensionsv1.CustomResourceDefinition{}
		if err := yaml.Unmarshal(data, crd); err != nil {
			return nil, err
		}
		for i := range crd.Spec.Versions {
			if crd.Spec.Versions[i].Schema != nil {
				stripXValidations(crd.Spec.Versions[i].Schema.OpenAPIV3Schema)
			}
		}
		crds = append(crds, crd)
	}
	return crds, nil
}

func stripXValidations(s *apiextensionsv1.JSONSchemaProps) {
	if s == nil {
		return
	}
	s.XValidations = nil
	for k := range s.Properties {
		c := s.Properties[k]
		stripXValidations(&c)
		s.Properties[k] = c
	}
	if s.Items != nil {
		stripXValidations(s.Items.Schema)
	}
	if s.AdditionalProperties != nil {
		stripXValidations(s.AdditionalProperties.Schema)
	}
}

func firstEnvTestBinDir() string {
	base := filepath.Join("..", "..", "bin", "k8s")
	entries, err := os.ReadDir(base)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() {
			return filepath.Join(base, e.Name())
		}
	}
	return ""
}
