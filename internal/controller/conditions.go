package controller

import (
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
)

// v1alpha1 statuses carried their own Condition type with SetCondition /
// GetCondition methods; v1alpha2 uses the standard []metav1.Condition, so
// these thin wrappers route through the apimachinery helpers, which also do
// the LastTransitionTime bookkeeping the old methods did by hand.

func upsertCondition(conds *[]metav1.Condition, generation int64, condType string, status metav1.ConditionStatus, reason, message string) {
	apimeta.SetStatusCondition(conds, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: generation,
	})
}

func setServerCondition(mcpServer *mcpv1alpha2.MCPServer, condType string, status metav1.ConditionStatus, reason, message string) {
	upsertCondition(&mcpServer.Status.Conditions, mcpServer.Generation, condType, status, reason, message)
}

func setGroupCondition(group *mcpv1alpha2.MCPServerGroup, condType string, status metav1.ConditionStatus, reason, message string) {
	upsertCondition(&group.Status.Conditions, group.Generation, condType, status, reason, message)
}

func getCondition(conds []metav1.Condition, condType string) *metav1.Condition {
	return apimeta.FindStatusCondition(conds, condType)
}
