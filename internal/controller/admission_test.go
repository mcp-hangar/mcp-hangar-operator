package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
)

// ---------------------------------------------------------------------------
// Wildcard-egress admission is enforced by the validating webhook, not the CRD.
//
// It previously lived as a CRD x-kubernetes-validations CEL rule, but that CEL
// referenced self.metadata.annotations, which CRD validation does not expose
// (only name/generateName). Recent apiservers reject such a CRD at install
// (#54), and the earlier Go-mirror test here gave false confidence because it
// re-implemented the CEL instead of exercising the real path. The authoritative
// tests now live in internal/webhook (TestValidateProvider_WildcardEgress*).
//
// The envtest cases below still verify that the API server accepts providers
// which do not trigger any CRD-schema rejection.
// ---------------------------------------------------------------------------

func TestAdmission_Envtest_NoCapsCreated(t *testing.T) {
	provider := &mcpv1alpha2.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "envtest-no-caps",
			Namespace: "default",
		},
		Spec: mcpv1alpha2.MCPServerSpec{
			Mode:  mcpv1alpha2.MCPServerModeContainer,
			Image: "test:latest",
		},
	}

	err := k8sClient.Create(ctx, provider)
	require.NoError(t, err, "provider without capabilities should be accepted by envtest API server")

	// Cleanup
	_ = k8sClient.Delete(ctx, provider)
}

// These two verify that a numeric CRD schema bound actually reaches the
// apiserver (#22) -- that the generated openAPIV3Schema is installed and
// enforced, not merely present in the repo.
//
// They used to drive spec.observability.metrics.port, which was removed in #121
// along with the rest of the observability declaration. The bound they exist to
// cover is not gone, so the probe moved rather than being deleted:
// spec.replicas carries Minimum=0 / Maximum=10 and is live.
func TestAdmission_Envtest_ReplicasOutOfRangeRejected(t *testing.T) {
	tooMany := int32(11)
	provider := &mcpv1alpha2.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "envtest-bad-replicas",
			Namespace: "default",
		},
		Spec: mcpv1alpha2.MCPServerSpec{
			Mode:     mcpv1alpha2.MCPServerModeContainer,
			Image:    "test:latest",
			Replicas: &tooMany,
		},
	}

	err := k8sClient.Create(ctx, provider)
	require.Error(t, err, "replicas 11 should be rejected by the CRD schema (Maximum=10)")
	_ = k8sClient.Delete(ctx, provider)
}

func TestAdmission_Envtest_ReplicasInRangeAccepted(t *testing.T) {
	inRange := int32(3)
	provider := &mcpv1alpha2.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "envtest-good-replicas",
			Namespace: "default",
		},
		Spec: mcpv1alpha2.MCPServerSpec{
			Mode:     mcpv1alpha2.MCPServerModeContainer,
			Image:    "test:latest",
			Replicas: &inRange,
		},
	}

	err := k8sClient.Create(ctx, provider)
	require.NoError(t, err, "replicas 3 should be accepted")
	_ = k8sClient.Delete(ctx, provider)
}

// ---------------------------------------------------------------------------
// Egress Audit Tests (unit tests using fakeEventRecorder)
// ---------------------------------------------------------------------------

func TestReconcileEgressAudit_WildcardOverrideEmitsWarning(t *testing.T) {
	provider := newTestProvider("egress-audit-wildcard", "default", &mcpv1alpha2.MCPServerCapabilities{
		Network: &mcpv1alpha2.NetworkCapabilitiesSpec{
			Egress: []mcpv1alpha2.EgressRuleSpec{
				{Host: "*", Port: 443, Protocol: "https"},
			},
		},
	})
	provider.Annotations = map[string]string{
		"hangar.io/allow-unrestricted-egress": "true",
	}

	r, fakeRec := newViolationTestReconciler(provider)

	r.reconcileEgressAudit(context.Background(), provider)

	require.Len(t, fakeRec.events, 1, "should emit exactly one event")
	assert.Contains(t, fakeRec.events[0], corev1.EventTypeWarning)
	assert.Contains(t, fakeRec.events[0], ReasonUnrestrictedEgressAllowed)
	assert.Contains(t, fakeRec.events[0], "wildcard egress")
}

func TestReconcileEgressAudit_NoWildcardNoEvent(t *testing.T) {
	provider := newTestProvider("egress-audit-specific", "default", &mcpv1alpha2.MCPServerCapabilities{
		Network: &mcpv1alpha2.NetworkCapabilitiesSpec{
			Egress: []mcpv1alpha2.EgressRuleSpec{
				{Host: "api.example.com", Port: 443, Protocol: "https"},
			},
		},
	})

	r, fakeRec := newViolationTestReconciler(provider)

	r.reconcileEgressAudit(context.Background(), provider)

	assert.Empty(t, fakeRec.events, "no event should be emitted for specific hosts")
}

func TestReconcileEgressAudit_NilCapabilitiesNoEvent(t *testing.T) {
	provider := newTestProvider("egress-audit-nil", "default", nil)

	r, fakeRec := newViolationTestReconciler(provider)

	r.reconcileEgressAudit(context.Background(), provider)

	assert.Empty(t, fakeRec.events, "no event should be emitted for nil capabilities")
}
