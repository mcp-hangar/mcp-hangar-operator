package controller

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
)

// A cached read of a ConfigMap or a Service starts a cluster-wide informer
// that holds every one of them in the operator's memory (#195). Discovery
// must read both through the API reader; here the cached client refuses them,
// so a read that went through the cache fails the test.
func newUncachedDiscoveryReconciler(t *testing.T, objs ...client.Object) *MCPDiscoverySourceReconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, mcpv1alpha2.AddToScheme(scheme))

	cachedRead := errors.New("read through the manager cache: starts a cluster-wide informer")
	refuse := func(obj runtime.Object) bool {
		switch obj.(type) {
		case *corev1.ConfigMap, *corev1.ConfigMapList, *corev1.Service, *corev1.ServiceList:
			return true
		}
		return false
	}
	cached := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if refuse(obj) {
					return cachedRead
				}
				return c.Get(ctx, key, obj, opts...)
			},
			List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if refuse(list) {
					return cachedRead
				}
				return c.List(ctx, list, opts...)
			},
		}).Build()
	return &MCPDiscoverySourceReconciler{
		Client:    cached,
		Scheme:    scheme,
		APIReader: fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build(),
	}
}

func TestDiscovery_ConfigMapIsReadUncached(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "providers", Namespace: "demo"},
		Data:       map[string]string{"providers.yaml": oneProviderYAML},
	}
	source := &mcpv1alpha2.MCPDiscoverySource{
		ObjectMeta: metav1.ObjectMeta{Name: "src", Namespace: "demo"},
		Spec: mcpv1alpha2.MCPDiscoverySourceSpec{
			Type:         mcpv1alpha2.DiscoveryTypeConfigMap,
			ConfigMapRef: &mcpv1alpha2.ConfigMapReference{Name: "providers"},
		},
	}
	r := newUncachedDiscoveryReconciler(t, cm, source)

	found, _, err := r.discoverConfigMap(context.Background(), source)

	require.NoError(t, err)
	assert.Len(t, found, 1)
}

func TestDiscovery_ServicesAreListedUncached(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "mcp", Namespace: "demo", Labels: map[string]string{"app": "mcp"}},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 8080}}},
	}
	source := &mcpv1alpha2.MCPDiscoverySource{
		ObjectMeta: metav1.ObjectMeta{Name: "src", Namespace: "demo"},
		Spec: mcpv1alpha2.MCPDiscoverySourceSpec{
			Type:             mcpv1alpha2.DiscoveryTypeServiceDiscovery,
			ServiceDiscovery: &mcpv1alpha2.ServiceDiscoveryConfig{Selector: map[string]string{"app": "mcp"}},
		},
	}
	r := newUncachedDiscoveryReconciler(t, svc, source)

	_, _, err := r.discoverServices(context.Background(), source)

	require.NoError(t, err)
}
