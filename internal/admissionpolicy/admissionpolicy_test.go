// Package admissionpolicy_test runs the ValidatingAdmissionPolicies in
// config/admissionpolicy (#219) against a real apiserver. It has its own
// envtest environment: a block-level image digest policy would refuse the
// unpinned test images the controller suite creates.
package admissionpolicy_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
	"github.com/mcp-hangar/operator/internal/webhook"
	"github.com/mcp-hangar/operator/pkg/networkpolicy"
)

const (
	// paramsNamespace is the placeholder namespace the raw manifests carry;
	// kustomize rewrites it to the operator's.
	paramsNamespace = "system"
	paramsName      = "admission-policy-params"
	testNamespace   = "default"
	pinnedImage     = "ghcr.io/example/app@sha256:0000000000000000000000000000000000000000000000000000000000000000"
)

var (
	ctx       = context.Background()
	k8sClient client.Client
	warnings  = &warningCollector{}
)

// warningCollector records the admission warnings the apiserver returns.
type warningCollector struct {
	mu   sync.Mutex
	msgs []string
}

func (w *warningCollector) HandleWarningHeader(_ int, _ string, text string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.msgs = append(w.msgs, text)
}

func (w *warningCollector) take() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := w.msgs
	w.msgs = nil
	return out
}

func TestMain(m *testing.M) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		panic(err)
	}
	if err := mcpv1alpha2.AddToScheme(scheme); err != nil {
		panic(err)
	}

	testEnv := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := testEnv.Start()
	if err != nil {
		panic("failed to start envtest: " + err.Error())
	}

	cfg = rest.CopyConfig(cfg)
	cfg.WarningHandler = warnings
	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		panic(err)
	}

	if err := installPolicies(); err != nil {
		panic("failed to install the admission policies: " + err.Error())
	}

	code := m.Run()
	_ = testEnv.Stop()
	os.Exit(code)
}

// installPolicies creates every object in config/admissionpolicy as written,
// plus the namespace the params ConfigMap names.
func installPolicies() error {
	if err := k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: paramsNamespace}}); err != nil {
		return err
	}
	dir := filepath.Join("..", "..", "config", "admissionpolicy")
	for _, name := range []string{"wildcard_egress.yaml", "image_digest.yaml", "params.yaml"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
		for {
			var u map[string]any
			if err := dec.Decode(&u); err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				return fmt.Errorf("%s: %w", name, err)
			}
			obj, err := decodeObject(u)
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			if err := k8sClient.Create(ctx, obj); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
		}
	}
	return nil
}

func decodeObject(u map[string]any) (client.Object, error) {
	var obj client.Object
	switch u["kind"] {
	case "ValidatingAdmissionPolicy":
		obj = &admissionregistrationv1.ValidatingAdmissionPolicy{}
	case "ValidatingAdmissionPolicyBinding":
		obj = &admissionregistrationv1.ValidatingAdmissionPolicyBinding{}
	case "ConfigMap":
		obj = &corev1.ConfigMap{}
	default:
		return nil, fmt.Errorf("unexpected kind %v", u["kind"])
	}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u, obj); err != nil {
		return nil, err
	}
	return obj, nil
}

// setDigestLevel writes imageDigestPolicy into the params ConfigMap and waits
// until the apiserver acts on it: policies and params reach admission through
// informers, so a write is not in force the moment it returns.
func setDigestLevel(t *testing.T, level string) {
	t.Helper()
	cm := &corev1.ConfigMap{}
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKey{Namespace: paramsNamespace, Name: paramsName}, cm))
	cm.Data = map[string]string{"imageDigestPolicy": level}
	require.NoError(t, k8sClient.Update(ctx, cm))

	probe := containerServer("probe", "ghcr.io/example/app:latest", nil)
	require.Eventually(t, func() bool {
		warnings.take()
		err := dryRunCreate(probe)
		warned := len(warnings.take()) > 0
		switch level {
		case "block":
			return err != nil
		case "warn":
			return err == nil && warned
		default:
			return err == nil && !warned
		}
	}, 30*time.Second, 200*time.Millisecond, "image digest level %q never took effect", level)
}

func dryRunCreate(obj client.Object) error {
	return k8sClient.Create(ctx, obj.DeepCopyObject().(client.Object), client.DryRunAll)
}

func containerServer(name, image string, annotations map[string]string) *mcpv1alpha2.MCPServer {
	return &mcpv1alpha2.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace, Annotations: annotations},
		Spec: mcpv1alpha2.MCPServerSpec{
			Mode:  mcpv1alpha2.MCPServerModeContainer,
			Image: image,
		},
	}
}

func withEgress(p *mcpv1alpha2.MCPServer, rules ...mcpv1alpha2.EgressRuleSpec) *mcpv1alpha2.MCPServer {
	p.Spec.Capabilities = &mcpv1alpha2.MCPServerCapabilities{
		Network: &mcpv1alpha2.NetworkCapabilitiesSpec{Egress: rules},
	}
	return p
}

const egressOptIn = "hangar.io/allow-unrestricted-egress"

// TestWildcardEgress_MatchesTheGoRule drives one table through
// networkpolicy.IsUnrestrictedEgress and through the policy, so the CEL copy
// cannot drift from the rule the webhook and the controller use (#137).
func TestWildcardEgress_MatchesTheGoRule(t *testing.T) {
	setDigestLevel(t, "off")

	cases := []struct {
		name        string
		rule        mcpv1alpha2.EgressRuleSpec
		annotations map[string]string
		denied      bool
	}{
		{"host star", mcpv1alpha2.EgressRuleSpec{Host: "*"}, nil, true},
		{"cidr 0.0.0.0/0", mcpv1alpha2.EgressRuleSpec{Host: "any", CIDR: "0.0.0.0/0"}, nil, true},
		{"cidr ::/0", mcpv1alpha2.EgressRuleSpec{Host: "any", CIDR: "::/0"}, nil, true},
		{"host 0.0.0.0/0", mcpv1alpha2.EgressRuleSpec{Host: "0.0.0.0/0"}, nil, true},
		{"host ::/0", mcpv1alpha2.EgressRuleSpec{Host: "::/0"}, nil, true},
		{"host non-canonical /0", mcpv1alpha2.EgressRuleSpec{Host: "10.0.0.0/0"}, nil, true},
		{"host /0 with spaces", mcpv1alpha2.EgressRuleSpec{Host: " 0.0.0.0/0 "}, nil, true},
		{"host IPv4-mapped /0", mcpv1alpha2.EgressRuleSpec{Host: "::ffff:10.0.0.1/0"}, nil, true},
		{"host leading-zero octet /0", mcpv1alpha2.EgressRuleSpec{Host: "010.0.0.0/0"}, nil, false},
		{"host /00", mcpv1alpha2.EgressRuleSpec{Host: "0.0.0.0/00"}, nil, false},
		{"cidr /10", mcpv1alpha2.EgressRuleSpec{Host: "corp", CIDR: "10.0.0.0/10"}, nil, false},
		{"host /10", mcpv1alpha2.EgressRuleSpec{Host: "10.0.0.0/10"}, nil, false},
		{"glob host", mcpv1alpha2.EgressRuleSpec{Host: "*.example.com"}, nil, false},
		{"plain host", mcpv1alpha2.EgressRuleSpec{Host: "api.example.com"}, nil, false},
		{"host star opted in", mcpv1alpha2.EgressRuleSpec{Host: "*"}, map[string]string{egressOptIn: "true"}, false},
		{"host star opt-in false", mcpv1alpha2.EgressRuleSpec{Host: "*"}, map[string]string{egressOptIn: "false"}, true},
		{"host star other annotation", mcpv1alpha2.EgressRuleSpec{Host: "*"}, map[string]string{"other": "true"}, true},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			goDenied := networkpolicy.IsUnrestrictedEgress(tc.rule) && tc.annotations[egressOptIn] != "true"
			require.Equal(t, tc.denied, goDenied, "the Go rule disagrees with the table")

			p := withEgress(containerServer(fmt.Sprintf("egress-%d", i), pinnedImage, tc.annotations), tc.rule)
			err := dryRunCreate(p)
			if tc.denied {
				require.Error(t, err)
				assert.Contains(t, err.Error(), egressOptIn)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestWildcardEgress_PlainServerIsAdmitted guards the CEL against an unguarded
// field access: with failurePolicy Fail, a runtime error denies every write.
func TestWildcardEgress_PlainServerIsAdmitted(t *testing.T) {
	setDigestLevel(t, "block")
	remote := &mcpv1alpha2.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "plain-remote", Namespace: testNamespace},
		Spec: mcpv1alpha2.MCPServerSpec{
			Mode:     mcpv1alpha2.MCPServerModeRemote,
			Endpoint: "https://mcp.example.com",
		},
	}
	require.NoError(t, dryRunCreate(remote))
	require.NoError(t, dryRunCreate(containerServer("plain-pinned", pinnedImage, nil)))
}

// TestImageDigest_MatchesTheGoRule runs one table at every level, through
// webhook.IsImageDigestPinned and through the two policies.
func TestImageDigest_MatchesTheGoRule(t *testing.T) {
	const optOut = "hangar.io/allow-mutable-image"
	cases := []struct {
		name        string
		image       string
		annotations map[string]string
		pinned      bool
	}{
		{"mutable tag", "ghcr.io/example/app:1.0", nil, false},
		{"no tag", "ghcr.io/example/app", nil, false},
		{"digest", pinnedImage, nil, true},
		{"tag and digest", "ghcr.io/example/app:1.0@sha256:" + strings.Repeat("0", 64), nil, true},
		{"opted out", "ghcr.io/example/app:1.0", map[string]string{optOut: "true"}, true},
		{"opt-out false", "ghcr.io/example/app:1.0", map[string]string{optOut: "false"}, false},
	}
	for _, level := range []string{"block", "warn", "off"} {
		setDigestLevel(t, level)
		for i, tc := range cases {
			t.Run(level+"/"+tc.name, func(t *testing.T) {
				require.Equal(t, tc.pinned, webhook.IsImageDigestPinned(tc.image, tc.annotations),
					"the Go rule disagrees with the table")

				warnings.take()
				err := dryRunCreate(containerServer(fmt.Sprintf("digest-%s-%d", level, i), tc.image, tc.annotations))
				got := warnings.take()
				switch {
				case tc.pinned || level == "off":
					require.NoError(t, err)
					assert.Empty(t, got)
				case level == "block":
					require.Error(t, err)
					assert.Contains(t, err.Error(), "is not digest-pinned")
				case level == "warn":
					require.NoError(t, err)
					require.Len(t, got, 1)
					assert.Contains(t, got[0], "is not digest-pinned")
				}
			})
		}
	}
}

// TestImageDigest_RemoteModeIsNotJudged: the Go rule runs only in container
// mode, where the image is used.
func TestImageDigest_RemoteModeIsNotJudged(t *testing.T) {
	setDigestLevel(t, "block")
	remote := &mcpv1alpha2.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "remote-with-image", Namespace: testNamespace},
		Spec: mcpv1alpha2.MCPServerSpec{
			Mode:     mcpv1alpha2.MCPServerModeRemote,
			Endpoint: "https://mcp.example.com",
			Image:    "ghcr.io/example/app:latest",
		},
	}
	require.NoError(t, dryRunCreate(remote))
}

// TestPolicies_RatchetOnUpdate: an object admitted before a rule was in force
// can still be updated (labels, finalizers) and deleted; changing what the
// rule looks at is judged again.
func TestPolicies_RatchetOnUpdate(t *testing.T) {
	setDigestLevel(t, "off")
	p := withEgress(
		containerServer("ratchet", "ghcr.io/example/app:1.0", map[string]string{egressOptIn: "true"}),
		mcpv1alpha2.EgressRuleSpec{Host: "*"},
	)
	p.Finalizers = []string{"mcp-hangar.io/test"}
	require.NoError(t, k8sClient.Create(ctx, p))

	// Withdraw the egress opt-in: that is judged, and refused.
	noOptIn := p.DeepCopy()
	noOptIn.Annotations = nil
	require.Error(t, k8sClient.Update(ctx, noOptIn, client.DryRunAll))

	setDigestLevel(t, "block")

	// A label alone is not judged, though the image is now out of policy.
	p.Labels = map[string]string{"team": "a"}
	require.NoError(t, k8sClient.Update(ctx, p))

	// A new mutable image is.
	retag := p.DeepCopy()
	retag.Spec.Image = "ghcr.io/example/app:2.0"
	require.Error(t, k8sClient.Update(ctx, retag, client.DryRunAll))

	// An object on its way out is not judged at all: the finalizer comes off in
	// the same write that withdraws the opt-in, which a live object could not do.
	require.NoError(t, k8sClient.Delete(ctx, p))
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(p), p))
	p.Finalizers = nil
	p.Annotations = nil
	require.NoError(t, k8sClient.Update(ctx, p))
}

// TestWildcardEgress_LegacyServerKeepsReconciling: a server stored before the
// policy existed, with wildcard egress and no opt-in, still takes writes that
// leave its egress and opt-in alone -- replicas, an endpoint or image the
// discovery controller rewrites on resync. Only an egress change is judged.
func TestWildcardEgress_LegacyServerKeepsReconciling(t *testing.T) {
	setDigestLevel(t, "off")

	binding := &admissionregistrationv1.ValidatingAdmissionPolicyBinding{}
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKey{Name: "wildcard-egress"}, binding))
	saved := binding.DeepCopy()
	require.NoError(t, k8sClient.Delete(ctx, binding))

	legacy := withEgress(containerServer("legacy-wildcard", "ghcr.io/example/app:1.0", nil),
		mcpv1alpha2.EgressRuleSpec{Host: "*"})
	probe := withEgress(containerServer("legacy-probe", pinnedImage, nil), mcpv1alpha2.EgressRuleSpec{Host: "*"})
	require.Eventually(t, func() bool {
		return dryRunCreate(probe) == nil
	}, 30*time.Second, 200*time.Millisecond, "the wildcard binding never stopped applying")
	require.NoError(t, k8sClient.Create(ctx, legacy))
	t.Cleanup(func() { _ = k8sClient.Delete(ctx, legacy) })

	restored := &admissionregistrationv1.ValidatingAdmissionPolicyBinding{
		ObjectMeta: metav1.ObjectMeta{Name: saved.Name},
		Spec:       saved.Spec,
	}
	require.NoError(t, k8sClient.Create(ctx, restored))
	require.Eventually(t, func() bool {
		return dryRunCreate(probe) != nil
	}, 30*time.Second, 200*time.Millisecond, "the wildcard binding never applied again")

	zero := int32(0)
	legacy.Spec.Replicas = &zero
	legacy.Spec.Image = "ghcr.io/example/app:2.0"
	require.NoError(t, k8sClient.Update(ctx, legacy), "a spec change outside egress must not be re-judged")

	narrowed := legacy.DeepCopy()
	narrowed.Spec.Capabilities.Network.Egress = append(narrowed.Spec.Capabilities.Network.Egress,
		mcpv1alpha2.EgressRuleSpec{Host: "api.example.com"})
	err := k8sClient.Update(ctx, narrowed, client.DryRunAll)
	require.Error(t, err, "an egress change is judged, and the wildcard is still there")
	assert.Contains(t, err.Error(), egressOptIn)
}
