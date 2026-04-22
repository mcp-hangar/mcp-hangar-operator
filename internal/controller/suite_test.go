package controller

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	mcpv1alpha1 "github.com/mcp-hangar/operator/api/v1alpha1"
	networkingv1 "k8s.io/api/networking/v1"
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

	// Configure envtest with generated CRDs from this repo.
	// For envtest we strip CEL x-kubernetes-validations because the local
	// control-plane version used in tests rejects metadata.annotations access.
	testEnv = &envtest.Environment{
		BinaryAssetsDirectory: filepath.Join("..", "..", "bin", "k8s", "1.29.0-linux-amd64"),
		CRDs:                  crds,
		ErrorIfCRDPathMissing: true,
	}

	cfg, err = testEnv.Start()
	if err != nil {
		panic("failed to start envtest: " + err.Error())
	}
	if cfg == nil {
		panic("envtest config is nil")
	}

	// Register CRD scheme
	if err := mcpv1alpha1.AddToScheme(scheme.Scheme); err != nil {
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

func loadTestCRDs() ([]*apiextensionsv1.CustomResourceDefinition, error) {
	base := filepath.Join("..", "..", "config", "crd", "bases")
	files := []string{
		"mcp-hangar.io_mcpdiscoverysources.yaml",
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

		for i := range crd.Spec.Versions {
			if crd.Spec.Versions[i].Schema == nil {
				continue
			}
			stripXValidations(crd.Spec.Versions[i].Schema.OpenAPIV3Schema)
		}

		crds = append(crds, crd)
	}

	return crds, nil
}

func stripXValidations(schema *apiextensionsv1.JSONSchemaProps) {
	if schema == nil {
		return
	}

	schema.XValidations = nil

	for key := range schema.Properties {
		child := schema.Properties[key]
		stripXValidations(&child)
		schema.Properties[key] = child
	}

	for i := range schema.AllOf {
		stripXValidations(&schema.AllOf[i])
	}
	for i := range schema.AnyOf {
		stripXValidations(&schema.AnyOf[i])
	}
	for i := range schema.OneOf {
		stripXValidations(&schema.OneOf[i])
	}
	if schema.Not != nil {
		stripXValidations(schema.Not)
	}
	if schema.Items != nil {
		stripXValidations(schema.Items.Schema)
		for i := range schema.Items.JSONSchemas {
			stripXValidations(&schema.Items.JSONSchemas[i])
		}
	}
	if schema.AdditionalProperties != nil {
		stripXValidations(schema.AdditionalProperties.Schema)
	}
	if schema.AdditionalItems != nil {
		stripXValidations(schema.AdditionalItems.Schema)
	}
	if schema.PatternProperties != nil {
		for key := range schema.PatternProperties {
			child := schema.PatternProperties[key]
			stripXValidations(&child)
			schema.PatternProperties[key] = child
		}
	}
	if schema.Dependencies != nil {
		for key := range schema.Dependencies {
			dep := schema.Dependencies[key]
			if dep.Schema != nil {
				stripXValidations(dep.Schema)
			}
			schema.Dependencies[key] = dep
		}
	}
	for i := range schema.Definitions {
		child := schema.Definitions[i]
		stripXValidations(&child)
		schema.Definitions[i] = child
	}
}
