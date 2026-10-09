package controller

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
)

// A policy's targets are resolved at reconcile time: an MCPServer by name, an
// MCPServerGroup to the members its selector matches right then. Nothing in
// the policy or its owned NetworkPolicy changes when a server joins the group
// later, or is deleted and recreated under the same name (which drops core's
// L7 policy with the server), so without a watch on the targets themselves a
// governed server ran with no backstop and no L7 policy until the informer
// resync, while the CR said Compiled (#190). The two map functions below are
// that watch. They read only the cache: the policies of one namespace, and
// for a group target the group object, filtered in memory.

// policiesForMCPServer maps an MCPServer to every MCPEgressPolicy in its
// namespace that targets it: by name, or through an MCPServerGroup whose
// selector matches the server's labels. EnqueueRequestsFromMapFunc maps both
// sides of an update, so a label change that moves a server out of a group
// still reaches the policy that used to cover it.
func (r *MCPEgressPolicyReconciler) policiesForMCPServer(ctx context.Context, obj client.Object) []reconcile.Request {
	server, ok := obj.(*mcpv1alpha2.MCPServer)
	if !ok {
		return nil
	}
	logger := log.FromContext(ctx)
	var policies mcpv1alpha2.MCPEgressPolicyList
	if err := r.List(ctx, &policies, client.InNamespace(server.Namespace)); err != nil {
		logger.Error(err, "list MCPEgressPolicies for MCPServer mapping", "server", client.ObjectKeyFromObject(server))
		return nil
	}

	serverLabels := labels.Set(server.Labels)
	// Several policies may target the same group; read and evaluate it once.
	groupMatches := map[string]bool{}
	selects := func(groupName string) bool {
		if matched, seen := groupMatches[groupName]; seen {
			return matched
		}
		matched := false
		group := &mcpv1alpha2.MCPServerGroup{}
		key := types.NamespacedName{Name: groupName, Namespace: server.Namespace}
		if err := r.Get(ctx, key, group); err != nil {
			// A missing group has no members to follow; the policy is already
			// on its TargetNotFound requeue.
			if !apierrors.IsNotFound(err) {
				logger.Error(err, "get MCPServerGroup for MCPServer mapping", "group", key)
			}
		} else if group.Spec.Selector != nil {
			sel, err := metav1.LabelSelectorAsSelector(group.Spec.Selector)
			if err != nil {
				logger.Error(err, "parse MCPServerGroup selector for MCPServer mapping", "group", key)
			} else {
				matched = sel.Matches(serverLabels)
			}
		}
		groupMatches[groupName] = matched
		return matched
	}

	var reqs []reconcile.Request
	for i := range policies.Items {
		p := &policies.Items[i]
		switch p.Spec.TargetRef.Kind {
		case "MCPServer":
			if p.Spec.TargetRef.Name != server.Name {
				continue
			}
		case "MCPServerGroup":
			if !selects(p.Spec.TargetRef.Name) {
				continue
			}
		default:
			continue
		}
		reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(p)})
	}
	return reqs
}

// policiesForMCPServerGroup maps an MCPServerGroup to every MCPEgressPolicy in
// its namespace whose targetRef names it, so a selector edit recomputes the
// members and a group that appears or disappears is picked up at once rather
// than on the next TargetNotFound requeue.
func (r *MCPEgressPolicyReconciler) policiesForMCPServerGroup(ctx context.Context, obj client.Object) []reconcile.Request {
	group, ok := obj.(*mcpv1alpha2.MCPServerGroup)
	if !ok {
		return nil
	}
	var policies mcpv1alpha2.MCPEgressPolicyList
	if err := r.List(ctx, &policies, client.InNamespace(group.Namespace)); err != nil {
		log.FromContext(ctx).Error(err, "list MCPEgressPolicies for MCPServerGroup mapping",
			"group", client.ObjectKeyFromObject(group))
		return nil
	}
	var reqs []reconcile.Request
	for i := range policies.Items {
		p := &policies.Items[i]
		if p.Spec.TargetRef.Kind != "MCPServerGroup" || p.Spec.TargetRef.Name != group.Name {
			continue
		}
		reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(p)})
	}
	return reqs
}
