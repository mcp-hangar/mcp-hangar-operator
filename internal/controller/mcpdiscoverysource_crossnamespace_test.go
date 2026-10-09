package controller

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
)

// createDiscoverySourceIn creates a ConfigMap-type source whose configMapRef
// names cmNamespace explicitly.
func createDiscoverySourceIn(t *testing.T, name, namespace, cmName, cmNamespace string) {
	t.Helper()
	source := &mcpv1alpha2.MCPDiscoverySource{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: mcpv1alpha2.MCPDiscoverySourceSpec{
			Type: mcpv1alpha2.DiscoveryTypeConfigMap,
			Mode: mcpv1alpha2.DiscoveryModeAdditive,
			ConfigMapRef: &mcpv1alpha2.ConfigMapReference{
				Name:      cmName,
				Namespace: cmNamespace,
			},
		},
	}
	require.NoError(t, k8sClient.Create(ctx, source))
}

// A source in namespace A pointing at a ConfigMap in namespace B must read
// nothing and create nothing (#234). The envtest suite registers no admission
// webhooks, so this is the webhooks-off path.
func TestMCPDiscoverySource_CrossNamespaceConfigMapRefused(t *testing.T) {
	nsA := createNamespace(t, "test-disc-xns-a")
	defer k8sClient.Delete(ctx, nsA)
	nsB := createNamespace(t, "test-disc-xns-b")
	defer k8sClient.Delete(ctx, nsB)

	sourceName := "cm-xns"
	cmName := "xns-cm"

	createConfigMap(t, cmName, nsB.Name, containerProviderYAML)
	createDiscoverySourceIn(t, sourceName, nsA.Name, cmName, nsB.Name)

	waitForDiscoveryCondition(t, sourceName, nsA.Name, ConditionSynced, metav1.ConditionFalse)

	source := &mcpv1alpha2.MCPDiscoverySource{}
	require.NoError(t, k8sClient.Get(ctx, types.NamespacedName{Name: sourceName, Namespace: nsA.Name}, source))

	synced := getCondition(source.Status.Conditions, ConditionSynced)
	require.NotNil(t, synced)
	assert.Equal(t, ReasonCrossNamespaceRefused, synced.Reason)
	assert.Contains(t, synced.Message, nsA.Name)
	assert.Contains(t, synced.Message, nsB.Name)

	ready := getCondition(source.Status.Conditions, ConditionReady)
	require.NotNil(t, ready)
	assert.Equal(t, metav1.ConditionFalse, ready.Status)
	assert.Equal(t, ReasonCrossNamespaceRefused, ready.Reason)

	// The refusal returns before any create, so the condition being set means
	// nothing was created -- in either namespace.
	for _, ns := range []string{nsA.Name, nsB.Name} {
		list := &mcpv1alpha2.MCPServerList{}
		require.NoError(t, k8sClient.List(ctx, list,
			client.InNamespace(ns),
			client.MatchingLabels{LabelDiscoveryManagedBy: sourceName},
		))
		assert.Empty(t, list.Items, "a cross-namespace ConfigMap must not become MCPServers in %s", ns)
	}
	assert.Empty(t, source.Status.DiscoveredMCPServers)
}

// Naming the source's own namespace explicitly is the same as leaving it empty.
func TestMCPDiscoverySource_SameNamespaceConfigMapRefExplicit(t *testing.T) {
	ns := createNamespace(t, "test-disc-samens")
	defer k8sClient.Delete(ctx, ns)

	sourceName := "cm-samens"
	cmName := "samens-cm"

	createConfigMap(t, cmName, ns.Name, containerProviderYAML)
	createDiscoverySourceIn(t, sourceName, ns.Name, cmName, ns.Name)

	waitForDiscoveryCondition(t, sourceName, ns.Name, ConditionSynced, metav1.ConditionTrue)
	waitForManagedProviderCount(t, sourceName, ns.Name, 2)
	assert.Equal(t, "busybox", getManagedServer(t, sourceName, ns.Name, "provider-c").Spec.Image)
}

// The Warning event is emitted on the transition into the refusal, not on
// every reconcile.
func TestMCPDiscoverySource_CrossNamespaceWarningOnce(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = mcpv1alpha2.AddToScheme(scheme)

	source := &mcpv1alpha2.MCPDiscoverySource{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "src",
			Namespace:  "team-a",
			Finalizers: []string{finalizerName},
		},
		Spec: mcpv1alpha2.MCPDiscoverySourceSpec{
			Type:         mcpv1alpha2.DiscoveryTypeConfigMap,
			ConfigMapRef: &mcpv1alpha2.ConfigMapReference{Name: "providers", Namespace: "team-b"},
		},
	}
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "providers", Namespace: "team-b"},
		Data:       map[string]string{"providers.yaml": containerProviderYAML},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(source, cm).
		WithStatusSubresource(&mcpv1alpha2.MCPDiscoverySource{}).
		Build()
	recorder := events.NewFakeRecorder(20)
	r := &MCPDiscoverySourceReconciler{Client: c, Scheme: scheme, Recorder: recorder}

	bg := context.Background()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "src", Namespace: "team-a"}}
	for range 3 {
		res, err := r.Reconcile(bg, req)
		require.NoError(t, err)
		assert.Zero(t, res.RequeueAfter)
	}

	list := &mcpv1alpha2.MCPServerList{}
	require.NoError(t, c.List(bg, list))
	assert.Empty(t, list.Items, "a cross-namespace ConfigMap must not become MCPServers")

	close(recorder.Events)
	var warnings, others []string
	for e := range recorder.Events {
		if strings.Contains(e, ReasonCrossNamespaceRefused) {
			warnings = append(warnings, e)
		} else {
			others = append(others, e)
		}
	}
	require.Len(t, warnings, 1, "one Warning on the transition, none on later reconciles")
	assert.True(t, strings.HasPrefix(warnings[0], corev1.EventTypeWarning))
	assert.Empty(t, others, "a refused source does not start a sync")
}
