package controller

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
	networkingv1 "k8s.io/api/networking/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/yaml"
)

var (
	testEnv                   *envtest.Environment
	k8sClient                 client.Client
	cfg                       *rest.Config
	ctx                       context.Context
	cancel                    context.CancelFunc
	enableMCPServerReconciler = false
)

func TestMain(m *testing.M) {
	logf.SetLogger(zap.New(zap.WriteTo(os.Stderr), zap.UseDevMode(true)))

	ctx, cancel = context.WithCancel(context.Background())

	crds, err := loadTestCRDs()
	if err != nil {
		panic("failed to load test CRDs: " + err.Error())
	}

	// Configure envtest with the generated CRDs from this repo, CEL rules and
	// all. They used to be stripped, because one rule read
	// `self.metadata.annotations` -- a field CRD validation CEL does not expose
	// -- and the control plane rejected the CRD. Stripping made the suite pass
	// and left the CRD uninstallable on any current cluster (#30, #54). The
	// rule is gone; handing the apiserver what we actually ship is the check
	// that would have caught it.
	testEnv = &envtest.Environment{
		CRDs:                  crds,
		ErrorIfCRDPathMissing: true,
	}

	// Resolve envtest binaries via KUBEBUILDER_ASSETS (set by
	// setup-envtest / `make test`), which works cross-platform. Only fall
	// back to a locally provisioned bin/k8s/<version> directory when the env
	// var is unset, and never hardcode a specific OS/arch.
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		testEnv.BinaryAssetsDirectory = firstFoundEnvTestBinaryDir()
	}

	cfg, err = testEnv.Start()
	if err != nil {
		panic("failed to start envtest: " + err.Error())
	}
	if cfg == nil {
		panic("envtest config is nil")
	}

	// Register CRD scheme
	if err := mcpv1alpha2.AddToScheme(scheme.Scheme); err != nil {
		panic("failed to add CRD scheme: " + err.Error())
	}
	// Ensure networking API is in scheme (required for NetworkPolicy Owns() watch)
	if err := networkingv1.AddToScheme(scheme.Scheme); err != nil {
		panic("failed to add networking scheme: " + err.Error())
	}

	// Create k8s client
	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	if err != nil {
		panic("failed to create k8s client: " + err.Error())
	}

	// Create manager with metrics disabled to avoid port conflicts
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:  scheme.Scheme,
		Metrics: metricsserver.Options{BindAddress: "0"},
	})
	if err != nil {
		panic("failed to create manager: " + err.Error())
	}

	// Register MCPServer controller (can be disabled for group/discovery tests)
	if enableMCPServerReconciler {
		if err := (&MCPServerReconciler{
			Client:   mgr.GetClient(),
			Scheme:   mgr.GetScheme(),
			Recorder: mgr.GetEventRecorderFor("mcpserver-controller"),
			Config:   DefaultReconcilerConfig(),
		}).SetupWithManager(mgr); err != nil {
			panic("failed to setup MCPServer controller: " + err.Error())
		}
	}

	// Register MCPServerGroup controller
	if err := (&MCPServerGroupReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("mcpservergroup-controller"),
	}).SetupWithManager(mgr); err != nil {
		panic("failed to setup MCPServerGroup controller: " + err.Error())
	}

	// Register MCPDiscoverySource controller
	if err := (&MCPDiscoverySourceReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("mcpdiscoverysource-controller"),
	}).SetupWithManager(mgr); err != nil {
		panic("failed to setup MCPDiscoverySource controller: " + err.Error())
	}

	// Start manager in background
	go func() {
		if err := mgr.Start(ctx); err != nil {
			panic("failed to start manager: " + err.Error())
		}
	}()

	// Run tests
	code := m.Run()

	// Cleanup
	cancel()
	if err := testEnv.Stop(); err != nil {
		panic("failed to stop envtest: " + err.Error())
	}
	os.Exit(code)
}

// firstFoundEnvTestBinaryDir returns the first entry under bin/k8s, which is
// where `setup-envtest` / `make envtest` provisions the control-plane binaries
// for the local platform. It returns an empty string if nothing is found, in
// which case envtest falls back to its own resolution (e.g. KUBEBUILDER_ASSETS).
func firstFoundEnvTestBinaryDir() string {
	basePath := filepath.Join("..", "..", "bin", "k8s")
	entries, err := os.ReadDir(basePath)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return filepath.Join(basePath, entry.Name())
		}
	}
	return ""
}

func loadTestCRDs() ([]*apiextensionsv1.CustomResourceDefinition, error) {
	// Every CRD this repo generates. mcpegresspolicies was missing, and it is
	// the only one carrying a CEL rule -- so the single rule in the tree was
	// unreached twice over: not loaded here, and stripped if it had been.
	base := filepath.Join("..", "..", "config", "crd", "bases")
	files := []string{
		"mcp-hangar.io_mcpdiscoverysources.yaml",
		"mcp-hangar.io_mcpegresspolicies.yaml",
		"mcp-hangar.io_mcpservergroups.yaml",
		"mcp-hangar.io_mcpservers.yaml",
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

		crds = append(crds, crd)
	}

	return crds, nil
}
