package controller

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
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

// l7AnswerCore is a stand-in core whose answer to an L7 push the test changes
// between reconciles.
type l7AnswerCore struct {
	mu   sync.Mutex
	code int
	body string
}

func (c *l7AnswerCore) answer(code int, body string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.code, c.body = code, body
}

func (c *l7AnswerCore) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	c.mu.Lock()
	code, body := c.code, c.body
	c.mu.Unlock()
	w.WriteHeader(code)
	_, _ = w.Write([]byte(body))
}

// newL7EnvtestReconciler creates a server and a policy targeting it in their
// own namespace on the suite's apiserver and returns a reconciler over the
// suite's client, so the status it writes passes the CRD's schema for real.
// hangarClient nil runs with core integration off.
func newL7EnvtestReconciler(t *testing.T, nsName string, hangarClient *hangar.Client) (*MCPEgressPolicyReconciler, ctrl.Request) {
	t.Helper()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}}
	if err := k8sClient.Create(ctx, ns); err != nil && !apierrors.IsAlreadyExists(err) {
		require.NoError(t, err)
	}
	srv := testServer("srv", nsName)
	require.NoError(t, k8sClient.Create(ctx, srv))
	policy := testPolicy("pol", nsName)
	require.NoError(t, k8sClient.Create(ctx, policy))
	t.Cleanup(func() {
		cur := &mcpv1alpha2.MCPEgressPolicy{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: "pol", Namespace: nsName}, cur); err == nil {
			cur.Finalizers = nil
			_ = k8sClient.Update(ctx, cur)
			_ = k8sClient.Delete(ctx, cur)
		}
		_ = k8sClient.Delete(ctx, srv)
	})
	r := &MCPEgressPolicyReconciler{
		Client:       k8sClient,
		Scheme:       scheme.Scheme,
		Recorder:     events.NewFakeRecorder(20),
		HangarClient: hangarClient,
	}
	return r, ctrl.Request{NamespacedName: types.NamespacedName{Name: "pol", Namespace: nsName}}
}

// l7EnvtestClient talks to core with one fast retry, so a 5xx costs the test
// milliseconds rather than the production backoff.
func l7EnvtestClient(url string) *hangar.Client {
	return hangar.NewClient(&hangar.Config{URL: url, MaxRetries: 1, BaseDelay: time.Millisecond})
}

func getPolicyStatus(t *testing.T, req ctrl.Request) *mcpv1alpha2.MCPEgressPolicy {
	t.Helper()
	out := &mcpv1alpha2.MCPEgressPolicy{}
	require.NoError(t, k8sClient.Get(ctx, req.NamespacedName, out))
	return out
}

// The #191 shape: core answers the push with 403 (a key without policy:write).
// The policy used to read Compiled=True, BackstopApplied=True, Degraded=False
// with a Warning Event as the only trace. It must now say the L7 half is not
// delivered, and why, and be Degraded for it -- with the status written before
// the push error propagates.
func TestEgressPolicy_L7PushRejected_ReportsNotDeliveredAndDegraded(t *testing.T) {
	core := httptest.NewServer(&l7AnswerCore{code: http.StatusForbidden, body: `{"error":"forbidden"}`})
	t.Cleanup(core.Close)
	r, req := newL7EnvtestReconciler(t, "l7-rejected", l7EnvtestClient(core.URL))

	_, err := r.Reconcile(ctx, req) // adds the finalizer
	require.NoError(t, err)
	_, err = r.Reconcile(ctx, req)
	require.Error(t, err, "a rejected push must still requeue")

	out := getPolicyStatus(t, req)
	l7 := condStatus(out, EgressPolicyConditionL7Delivered)
	require.NotNil(t, l7, "no L7Delivered condition: the policy reads all green while core holds nothing")
	assert.Equal(t, metav1.ConditionFalse, l7.Status)
	assert.Equal(t, "CoreAuthRejected", l7.Reason)
	assert.Contains(t, l7.Message, `"srv"`, "the member whose push failed is named")
	assert.Contains(t, l7.Message, "403")

	deg := condStatus(out, EgressPolicyConditionDegraded)
	require.NotNil(t, deg)
	assert.Equal(t, metav1.ConditionTrue, deg.Status)
	assert.Equal(t, "L7PushFailed", deg.Reason)

	// What the operator did do is still reported as done.
	assert.Equal(t, metav1.ConditionTrue, condStatus(out, EgressPolicyConditionCompiled).Status)
	assert.Equal(t, metav1.ConditionTrue, condStatus(out, EgressPolicyConditionBackstopApplied).Status)
}

// A core outage is a different reason from a refusal, and clears by itself:
// 503 reads PushFailed, the next good push reads Delivered and lifts Degraded.
func TestEgressPolicy_L7PushFailsThenSucceeds(t *testing.T) {
	answers := &l7AnswerCore{code: http.StatusServiceUnavailable, body: "down"}
	core := httptest.NewServer(answers)
	t.Cleanup(core.Close)
	r, req := newL7EnvtestReconciler(t, "l7-recovers", l7EnvtestClient(core.URL))

	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	_, err = r.Reconcile(ctx, req)
	require.Error(t, err)

	out := getPolicyStatus(t, req)
	require.NotNil(t, condStatus(out, EgressPolicyConditionL7Delivered))
	assert.Equal(t, metav1.ConditionFalse, condStatus(out, EgressPolicyConditionL7Delivered).Status)
	assert.Equal(t, "PushFailed", condStatus(out, EgressPolicyConditionL7Delivered).Reason)
	assert.Equal(t, "L7PushFailed", condStatus(out, EgressPolicyConditionDegraded).Reason)

	answers.answer(http.StatusOK, `{"l7_policy_set":true,"persisted":true}`)
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)

	out = getPolicyStatus(t, req)
	assert.Equal(t, metav1.ConditionTrue, condStatus(out, EgressPolicyConditionL7Delivered).Status)
	assert.Equal(t, "Delivered", condStatus(out, EgressPolicyConditionL7Delivered).Reason)
	assert.Equal(t, metav1.ConditionFalse, condStatus(out, EgressPolicyConditionDegraded).Status,
		"a push that succeeded must not leave Degraded behind")
}

// Nothing listening at the URL is CoreUnreachable, not a refusal.
func TestEgressPolicy_L7CoreUnreachable(t *testing.T) {
	core := httptest.NewServer(&l7AnswerCore{code: http.StatusOK, body: `{}`})
	core.Close()
	r, req := newL7EnvtestReconciler(t, "l7-unreachable", l7EnvtestClient(core.URL))

	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	_, err = r.Reconcile(ctx, req)
	require.Error(t, err)

	out := getPolicyStatus(t, req)
	require.NotNil(t, condStatus(out, EgressPolicyConditionL7Delivered))
	assert.Equal(t, metav1.ConditionFalse, condStatus(out, EgressPolicyConditionL7Delivered).Status)
	assert.Equal(t, "CoreUnreachable", condStatus(out, EgressPolicyConditionL7Delivered).Reason)
	assert.Equal(t, "L7PushFailed", condStatus(out, EgressPolicyConditionDegraded).Reason)
}

// core took the policy but says it will not survive a gateway restart (#1306):
// delivered, so True, but the reason says the durability gap is there.
func TestEgressPolicy_L7DeliveredNotPersisted(t *testing.T) {
	core := httptest.NewServer(&l7AnswerCore{code: http.StatusOK, body: `{"l7_policy_set":true,"persisted":false}`})
	t.Cleanup(core.Close)
	r, req := newL7EnvtestReconciler(t, "l7-not-persisted", l7EnvtestClient(core.URL))

	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)

	out := getPolicyStatus(t, req)
	l7 := condStatus(out, EgressPolicyConditionL7Delivered)
	require.NotNil(t, l7)
	assert.Equal(t, metav1.ConditionTrue, l7.Status)
	assert.Equal(t, "DeliveredNotPersisted", l7.Reason)
	assert.Contains(t, l7.Message, "Ready")
	assert.Equal(t, metav1.ConditionFalse, condStatus(out, EgressPolicyConditionDegraded).Status)
}

// Without --hangar-url there is nothing to deliver to: Unknown, not a failure.
func TestEgressPolicy_L7CoreIntegrationOff(t *testing.T) {
	r, req := newL7EnvtestReconciler(t, "l7-no-core", nil)

	_, err := r.Reconcile(ctx, req) // no finalizer without core integration, so one pass does the work
	require.NoError(t, err)

	out := getPolicyStatus(t, req)
	l7 := condStatus(out, EgressPolicyConditionL7Delivered)
	require.NotNil(t, l7)
	assert.Equal(t, metav1.ConditionUnknown, l7.Status)
	assert.Equal(t, "CoreIntegrationOff", l7.Reason)
	assert.Equal(t, metav1.ConditionFalse, condStatus(out, EgressPolicyConditionDegraded).Status)
}
