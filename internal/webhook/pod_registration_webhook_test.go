package webhook_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	mcpv1alpha1 "github.com/mcp-hangar/operator/api/v1alpha1"
	"github.com/mcp-hangar/operator/internal/webhook"
)

func podRegScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(s))
	require.NoError(t, mcpv1alpha1.AddToScheme(s))
	return s
}

func podRequest(t *testing.T, pod *corev1.Pod) admission.Request {
	t.Helper()
	raw, err := json.Marshal(pod)
	require.NoError(t, err)
	return admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
		Namespace: pod.Namespace,
		Object:    runtime.RawExtension{Raw: raw},
	}}
}

func providerPod(name, namespace, provider string) *corev1.Pod {
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
	if provider != "" {
		p.Labels = map[string]string{"mcp-hangar.io/provider": provider}
	}
	return p
}

func newValidator(t *testing.T, objs ...client.Object) *webhook.PodRegistrationValidator {
	t.Helper()
	s := podRegScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()
	return &webhook.PodRegistrationValidator{Client: c, Decoder: admission.NewDecoder(s)}
}

func TestPodRegistration_NoProviderLabel_Allowed(t *testing.T) {
	v := newValidator(t)
	resp := v.Handle(context.Background(), podRequest(t, providerPod("plain", "demo", "")))
	assert.True(t, resp.Allowed)
}

func TestPodRegistration_UnregisteredProvider_Denied(t *testing.T) {
	v := newValidator(t) // no MCPServer in the fake client
	resp := v.Handle(context.Background(), podRequest(t, providerPod("ghost", "demo", "shadow")))
	require.False(t, resp.Allowed, "a provider pod with no MCPServer must be denied")
	assert.Contains(t, resp.Result.Message, "no registered MCPServer")
}

func TestPodRegistration_RegisteredProvider_Allowed(t *testing.T) {
	server := &mcpv1alpha1.MCPServer{ObjectMeta: metav1.ObjectMeta{Name: "known", Namespace: "demo"}}
	v := newValidator(t, server)
	resp := v.Handle(context.Background(), podRequest(t, providerPod("worker", "demo", "known")))
	assert.True(t, resp.Allowed, "a provider pod backed by an MCPServer is admitted")
}

func TestPodRegistration_RegisteredInOtherNamespace_Denied(t *testing.T) {
	// MCPServer "known" exists but in a different namespace -> still denied.
	server := &mcpv1alpha1.MCPServer{ObjectMeta: metav1.ObjectMeta{Name: "known", Namespace: "other"}}
	v := newValidator(t, server)
	resp := v.Handle(context.Background(), podRequest(t, providerPod("worker", "demo", "known")))
	require.False(t, resp.Allowed, "registration is per-namespace")
	assert.Contains(t, resp.Result.Message, "no registered MCPServer")
}
