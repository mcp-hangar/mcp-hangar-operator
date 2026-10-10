package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/discovery"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
)

// `kubectl get mcpservers --field-selector spec.mode=remote` needs the CRD's
// selectableFields (#224). The apiserver honours them from 1.31 (beta) and
// ignores them on 1.30, which the floor job runs, so the test skips there.
func requireSelectableFields(t *testing.T) {
	t.Helper()
	dc, err := discovery.NewDiscoveryClientForConfig(cfg)
	require.NoError(t, err)
	v, err := dc.ServerVersion()
	require.NoError(t, err)
	if v.Major == "1" && len(v.Minor) == 2 && v.Minor < "31" {
		t.Skipf("apiserver %s.%s does not serve CRD selectableFields", v.Major, v.Minor)
	}
}

func names[T client.Object](items []T) []string {
	out := make([]string, 0, len(items))
	for _, i := range items {
		out = append(out, i.GetName())
	}
	return out
}

func TestSelectableFields_MCPServer(t *testing.T) {
	requireSelectableFields(t)
	ns := createNamespace(t, "test-selectable-srv").Name

	cold := testServer("cold", ns)
	remote := &mcpv1alpha2.MCPServer{Spec: mcpv1alpha2.MCPServerSpec{
		Mode: mcpv1alpha2.MCPServerModeRemote, Endpoint: "https://api.example.com/mcp",
	}}
	remote.Name, remote.Namespace = "remote", ns
	for _, s := range []*mcpv1alpha2.MCPServer{cold, remote} {
		require.NoError(t, createAndCleanup(t, s))
	}

	byMode := &mcpv1alpha2.MCPServerList{}
	require.NoError(t, k8sClient.List(ctx, byMode, client.InNamespace(ns),
		client.MatchingFields{"spec.mode": string(mcpv1alpha2.MCPServerModeRemote)}))
	got := make([]*mcpv1alpha2.MCPServer, 0, len(byMode.Items))
	for i := range byMode.Items {
		got = append(got, &byMode.Items[i])
	}
	assert.Equal(t, []string{"remote"}, names(got))

	// The suite runs no MCPServer reconciler, so write the state it would.
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(cold), cold))
	cold.Status.State = mcpv1alpha2.MCPServerStateCold
	require.NoError(t, k8sClient.Status().Update(ctx, cold))

	byState := &mcpv1alpha2.MCPServerList{}
	require.NoError(t, k8sClient.List(ctx, byState, client.InNamespace(ns),
		client.MatchingFields{"status.state": string(mcpv1alpha2.MCPServerStateCold)}))
	got = got[:0]
	for i := range byState.Items {
		got = append(got, &byState.Items[i])
	}
	assert.Equal(t, []string{"cold"}, names(got))
}

func TestSelectableFields_MCPEgressPolicy(t *testing.T) {
	requireSelectableFields(t)
	ns := createNamespace(t, "test-selectable-pol").Name

	audit := testPolicy("audit", ns)
	enforce := testPolicy("enforce", ns)
	enforce.Spec.Mode = mcpv1alpha2.EgressPolicyModeEnforce
	group := testPolicy("group", ns)
	group.Spec.TargetRef = mcpv1alpha2.EgressTargetRef{Kind: "MCPServerGroup", Name: "g"}
	for _, p := range []*mcpv1alpha2.MCPEgressPolicy{audit, enforce, group} {
		require.NoError(t, createAndCleanup(t, p))
	}

	list := func(field, value string) []string {
		l := &mcpv1alpha2.MCPEgressPolicyList{}
		require.NoError(t, k8sClient.List(ctx, l, client.InNamespace(ns), client.MatchingFields{field: value}))
		items := make([]*mcpv1alpha2.MCPEgressPolicy, 0, len(l.Items))
		for i := range l.Items {
			items = append(items, &l.Items[i])
		}
		return names(items)
	}
	assert.Equal(t, []string{"enforce"}, list("spec.mode", string(mcpv1alpha2.EgressPolicyModeEnforce)))
	assert.Equal(t, []string{"group"}, list("spec.targetRef.kind", "MCPServerGroup"))
}
