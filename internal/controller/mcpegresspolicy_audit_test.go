package controller

import (
	"context"
	"testing"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestEgressPolicy_AuditEnforceAudit_RemovesBothFlavors(t *testing.T) {
	for _, agent := range []string{"calico-node", "cilium"} {
		t.Run(agent, func(t *testing.T) {
			p := cidrPolicy("pol")
			p.Spec.Mode = "" // absent mode has the same Audit contract as CRD defaulting
			unrelated := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "independent", Namespace: "default"}}
			r := newCiliumCRDReconciler(t, testServer("srv", "default"), p, agentDaemonSet(agent), unrelated)
			stages := []struct {
				mode   mcpv1alpha2.EgressPolicyMode
				action mcpv1alpha2.EgressPolicyAction
			}{
				{"", mcpv1alpha2.EgressActionDeny},
				{mcpv1alpha2.EgressPolicyModeEnforce, mcpv1alpha2.EgressActionDeny},
				{mcpv1alpha2.EgressPolicyModeAudit, mcpv1alpha2.EgressActionDeny},
				{mcpv1alpha2.EgressPolicyModeEnforce, mcpv1alpha2.EgressActionDeny},
				{mcpv1alpha2.EgressPolicyModeEnforce, mcpv1alpha2.EgressActionAllow},
			}
			for i, stage := range stages {
				if i > 0 {
					require.NoError(t, r.Get(context.Background(), types.NamespacedName{Name: p.Name, Namespace: p.Namespace}, p))
					p.Spec.Mode, p.Spec.DefaultAction = stage.mode, stage.action
					p.Generation++
					require.NoError(t, r.Update(context.Background(), p))
				}
				out := reconcilePolicy(t, r, p)
				_, err := getBackstop(t, r, "pol", "default")
				if stage.mode == mcpv1alpha2.EgressPolicyModeEnforce && stage.action != mcpv1alpha2.EgressActionAllow {
					assert.Equal(t, agent == "cilium", ciliumBackstopExists(t, r, "pol"))
					if agent == "calico-node" {
						require.NoError(t, err)
					} else {
						require.True(t, apierrors.IsNotFound(err))
					}
					assert.Equal(t, metav1.ConditionTrue, condStatus(out, EgressPolicyConditionBackstopApplied).Status)
				} else {
					assert.True(t, apierrors.IsNotFound(err))
					assert.False(t, ciliumBackstopExists(t, r, "pol"))
					assert.Equal(t, mcpv1alpha2.BackstopDisabled, out.Status.BackstopEnforcement)
					reason := "AuditMode"
					if stage.action == mcpv1alpha2.EgressActionAllow {
						reason = "DefaultAllow"
					}
					assert.Equal(t, reason, condStatus(out, EgressPolicyConditionBackstopApplied).Reason)
					assert.Equal(t, metav1.ConditionFalse, condStatus(out, EgressPolicyConditionBackstopApplied).Status)
					assert.Equal(t, reason, condStatus(out, EgressPolicyConditionBackstopEnforceable).Reason)
					assert.Equal(t, metav1.ConditionFalse, condStatus(out, EgressPolicyConditionDegraded).Status)
				}
				require.NoError(t, r.Get(context.Background(), types.NamespacedName{Name: unrelated.Name, Namespace: unrelated.Namespace}, &networkingv1.NetworkPolicy{}))
			}
		})
	}
}
