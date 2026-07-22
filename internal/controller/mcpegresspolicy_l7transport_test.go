package controller

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
	"github.com/mcp-hangar/operator/pkg/hangar"
	"github.com/mcp-hangar/operator/pkg/networkpolicy"
)

func TestCompileL7Policy_MergesUpstreams(t *testing.T) {
	p := testPolicy("pol", "default")
	p.Spec.DefaultAction = mcpv1alpha2.EgressActionDeny
	p.Spec.Upstreams = []mcpv1alpha2.UpstreamRule{
		{
			Name:  "a",
			Match: mcpv1alpha2.UpstreamMatch{Host: "a.com"},
			Tools: &mcpv1alpha2.ToolRules{Allow: []string{"get_*"}, RequireApproval: []string{"create_*"}},
			Arguments: &mcpv1alpha2.ArgumentRules{
				Deny: &mcpv1alpha2.ArgumentDenyRules{SecretPatterns: []string{"aws-keys"}, MaxPayloadBytes: 1000},
			},
		},
		{
			Name:  "b",
			Match: mcpv1alpha2.UpstreamMatch{Host: "b.com"},
			Tools: &mcpv1alpha2.ToolRules{Allow: []string{"get_*", "list_*"}, Deny: []string{"delete_*"}},
			Arguments: &mcpv1alpha2.ArgumentRules{
				Deny: &mcpv1alpha2.ArgumentDenyRules{SecretPatterns: []string{"jwt"}, MaxPayloadBytes: 500},
			},
		},
	}

	out := compileL7Policy(p)

	assert.ElementsMatch(t, []string{"get_*", "list_*"}, out.Tools.Allow) // union, deduped
	assert.Equal(t, []string{"delete_*"}, out.Tools.Deny)
	assert.Equal(t, []string{"create_*"}, out.Tools.RequireApproval)
	assert.ElementsMatch(t, []string{"aws-keys", "jwt"}, out.Arguments.SecretPatterns)
	require.NotNil(t, out.Arguments.MaxPayloadBytes)
	assert.Equal(t, int64(500), *out.Arguments.MaxPayloadBytes) // most restrictive
	assert.Equal(t, "Deny", out.DefaultAction)
}

func TestCompileL7Policy_DefaultsToDeny(t *testing.T) {
	p := testPolicy("pol", "default") // no DefaultAction, no upstreams
	out := compileL7Policy(p)
	assert.Equal(t, "Deny", out.DefaultAction)
	assert.Nil(t, out.Arguments.MaxPayloadBytes)
}

func TestCompileL7Policy_ModeDefaultsToAudit(t *testing.T) {
	p := testPolicy("pol", "default") // no Mode set
	out := compileL7Policy(p)
	assert.Equal(t, "Audit", out.Mode)
}

func TestCompileL7Policy_ModePassesThroughEnforce(t *testing.T) {
	p := testPolicy("pol", "default")
	p.Spec.Mode = mcpv1alpha2.EgressPolicyModeEnforce
	out := compileL7Policy(p)
	assert.Equal(t, "Enforce", out.Mode)
}

func TestProviderNamesFromSelector(t *testing.T) {
	single := metav1.LabelSelector{MatchLabels: map[string]string{networkpolicy.LabelProvider: "srv"}}
	assert.Equal(t, []string{"srv"}, providerNamesFromSelector(single))

	group := metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{
		Key: networkpolicy.LabelProvider, Operator: metav1.LabelSelectorOpIn, Values: []string{"a", "b"},
	}}}
	assert.Equal(t, []string{"a", "b"}, providerNamesFromSelector(group))

	assert.Nil(t, providerNamesFromSelector(metav1.LabelSelector{}))
}

// End-to-end for the sender: reconciling a policy with core integration on makes
// the operator POST the compiled L7 policy to core at the right path.
func TestEgressPolicy_PushesL7ToCore(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"l7_policy_set":true}`))
	}))
	defer srv.Close()

	p := testPolicy("pol", "default")
	p.Spec.Upstreams = []mcpv1alpha2.UpstreamRule{{
		Name:  "gh",
		Match: mcpv1alpha2.UpstreamMatch{Host: "10.0.0.0/8"},
		Tools: &mcpv1alpha2.ToolRules{Allow: []string{"get_*"}},
	}}
	r := newEgressReconciler(testServer("srv", "default"), p)
	r.HangarClient = hangar.NewClient(&hangar.Config{URL: srv.URL, MaxRetries: 0})

	ctx := context.Background()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "pol", Namespace: "default"}}
	// First reconcile adds the finalizer and requeues; second does the work + push.
	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)

	assert.Equal(t, http.MethodPost, gotMethod)
	assert.Equal(t, "/api/mcp_servers/srv/l7_policy", gotPath)
	var payload hangar.L7PolicyPayload
	require.NoError(t, json.Unmarshal(gotBody, &payload))
	assert.Equal(t, []string{"get_*"}, payload.Tools.Allow)
	assert.Equal(t, "Deny", payload.DefaultAction)
}

// Deleting a policy clears the L7 policy in core and drops the finalizer.
func TestEgressPolicy_ClearsL7OnDelete(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := testPolicy("pol", "default")
	now := metav1.Now()
	p.DeletionTimestamp = &now
	p.Finalizers = []string{l7PolicyFinalizer}
	r := newEgressReconciler(testServer("srv", "default"), p)
	r.HangarClient = hangar.NewClient(&hangar.Config{URL: srv.URL, MaxRetries: 0})

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "pol", Namespace: "default"},
	})
	require.NoError(t, err)

	assert.Equal(t, http.MethodDelete, gotMethod)
	assert.Equal(t, "/api/mcp_servers/srv/l7_policy", gotPath)
}

// A persistent clear failure (core unreachable / auth error) must NOT wedge
// deletion: the finalizer is released best-effort so the policy is not stuck
// Terminating until someone hand-edits the finalizer.
func TestEgressPolicy_DeleteNotWedgedByClearFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError) // clear always fails
	}))
	defer srv.Close()

	p := testPolicy("pol", "default")
	now := metav1.Now()
	p.DeletionTimestamp = &now
	p.Finalizers = []string{l7PolicyFinalizer}
	r := newEgressReconciler(testServer("srv", "default"), p)
	r.HangarClient = hangar.NewClient(&hangar.Config{URL: srv.URL, MaxRetries: 0})

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "pol", Namespace: "default"},
	})
	require.NoError(t, err) // clear failed, but deletion must still complete

	// The finalizer is released, so the fake client completes deletion.
	var got mcpv1alpha2.MCPEgressPolicy
	getErr := r.Get(context.Background(), types.NamespacedName{Name: "pol", Namespace: "default"}, &got)
	assert.True(t, apierrors.IsNotFound(getErr),
		"policy should be deleted once the finalizer is released; getErr=%v finalizers=%v", getErr, got.Finalizers)
}
