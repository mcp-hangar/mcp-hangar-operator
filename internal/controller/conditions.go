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

// ReasonUnspecified stands in for a condition reason that is empty at runtime.
//
// metav1.Condition.Reason is validated MinLength=1 by the CRD schema, so an
// empty reason does not merely spoil one condition: the apiserver rejects the
// whole status subresource write, discarding every other field the reconcile
// just computed. That is how a healthy MCPServer sat at Initializing forever
// while its pod was Ready (#174) -- the success path was the only path that
// passed "" as a reason, so the one transition that mattered was the one that
// could never be persisted. Nothing in the type system objects: the compiler
// is perfectly happy with "".
//
// Substituting here bounds the damage of a reason that is empty at runtime --
// computed from a variable, say -- to one visibly wrong reason on one
// condition instead of a status that never persists at all. It is deliberately
// NOT the whole guard, because on its own it would turn a programming error
// into a silent one: an empty reason written as a literal at a call site is
// caught by TestNoCallSitePassesAnEmptyConditionReason, which fails the build
// rather than letting this fallback paper over it.
const ReasonUnspecified = "Unspecified"

func upsertCondition(conds *[]metav1.Condition, generation int64, condType string, status metav1.ConditionStatus, reason, message string) {
	if reason == "" {
		reason = ReasonUnspecified
	}

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
