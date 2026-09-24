package controller

import (
	"context"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
)

// DefaultGatewayPodSelector matches the pods of the core gateway as the
// mcp-hangar chart labels them (app.kubernetes.io/name is the chart name).
const DefaultGatewayPodSelector = "app.kubernetes.io/name=mcp-hangar"

// The compiled L7 policy lives in the gateway. On an install with no durable
// persistence backend (the chart default) a gateway pod that restarts comes
// back Ready holding no policy, while every MCPEgressPolicy still reports
// Compiled and nothing about the CR or its owned NetworkPolicy has changed.
// Without a signal from the gateway itself, re-delivery waited for the
// informer resync -- 2h16m in the environment that found it
// (mcp-hangar/mcp-hangar#1306). So a gateway pod turning Ready is that signal.

// gatewayPodBecameReady passes a pod matching sel at the moment it becomes
// Ready: a Ready pod first seen (a new pod, or every pod at operator start) and
// a transition from not Ready to Ready (a container restart in place). A pod
// that stays Ready, goes unready or is deleted has nothing to receive.
func gatewayPodBecameReady(sel labels.Selector) predicate.Funcs {
	matches := func(obj client.Object) (*corev1.Pod, bool) {
		pod, ok := obj.(*corev1.Pod)
		if !ok || sel == nil || !sel.Matches(labels.Set(pod.GetLabels())) {
			return nil, false
		}
		return pod, true
	}
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			pod, ok := matches(e.Object)
			return ok && podIsReady(pod)
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			newPod, ok := matches(e.ObjectNew)
			if !ok || !podIsReady(newPod) {
				return false
			}
			oldPod, ok := e.ObjectOld.(*corev1.Pod)
			return !ok || !podIsReady(oldPod)
		},
		DeleteFunc:  func(event.DeleteEvent) bool { return false },
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
}

// podIsReady reports whether the pod's Ready condition is True -- the point at
// which the gateway has restored what it can and is served traffic.
func podIsReady(pod *corev1.Pod) bool {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// policiesForGatewayPod maps a gateway pod to every MCPEgressPolicy. The
// operator talks to exactly one gateway (--hangar-url), so every policy it
// pushes targets the gateway this pod belongs to, whatever its namespace.
func (r *MCPEgressPolicyReconciler) policiesForGatewayPod(ctx context.Context, pod client.Object) []reconcile.Request {
	var list mcpv1alpha2.MCPEgressPolicyList
	if err := r.List(ctx, &list); err != nil {
		log.FromContext(ctx).Error(err, "list MCPEgressPolicies for gateway re-delivery",
			"pod", client.ObjectKeyFromObject(pod))
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{
			Name: list.Items[i].Name, Namespace: list.Items[i].Namespace,
		}})
	}
	if len(reqs) > 0 {
		log.FromContext(ctx).Info("Gateway pod became Ready; re-delivering L7 policies",
			"pod", client.ObjectKeyFromObject(pod), "policies", len(reqs))
	}
	return reqs
}

// gatewayPodHandler enqueues every policy for the pod an event carries. It
// maps only the new object of an update: EnqueueRequestsFromMapFunc maps both
// the old and the new one, which would list the policies twice per transition.
func (r *MCPEgressPolicyReconciler) gatewayPodHandler() handler.Funcs {
	enqueue := func(ctx context.Context, pod client.Object, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
		for _, req := range r.policiesForGatewayPod(ctx, pod) {
			q.Add(req)
		}
	}
	return handler.Funcs{
		CreateFunc: func(ctx context.Context, e event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			enqueue(ctx, e.Object, q)
		},
		UpdateFunc: func(ctx context.Context, e event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			enqueue(ctx, e.ObjectNew, q)
		},
	}
}

// gatewayCheckInterval bounds how often a push re-checks that the gateway
// selector still matches a pod, and so how often a zero match is logged.
const gatewayCheckInterval = 10 * time.Minute

// gatewayMatchCheck remembers when the selector was last checked.
type gatewayMatchCheck struct {
	mu   sync.Mutex
	last time.Time
}

// gatewayWatchEnabled reports whether the gateway pod watch is installed.
func (r *MCPEgressPolicyReconciler) gatewayWatchEnabled() bool {
	return r.HangarClient != nil && r.GatewayPodSelector != nil && !r.GatewayPodSelector.Empty()
}

// warnIfGatewayUnmatched logs an error-level line when the gateway selector
// matches no pod. The watch then sees no gateway at all and re-delivery on
// restart silently does nothing -- the default selector on a gateway that is
// not labelled the way the chart labels it. force checks regardless of when
// the last check ran (startup); otherwise one check per gatewayCheckInterval.
func (r *MCPEgressPolicyReconciler) warnIfGatewayUnmatched(ctx context.Context, force bool) {
	if !r.gatewayWatchEnabled() {
		return
	}
	r.gatewayCheck.mu.Lock()
	if !force && !r.gatewayCheck.last.IsZero() && time.Since(r.gatewayCheck.last) < gatewayCheckInterval {
		r.gatewayCheck.mu.Unlock()
		return
	}
	r.gatewayCheck.last = time.Now()
	r.gatewayCheck.mu.Unlock()

	logger := log.FromContext(ctx)
	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.MatchingLabelsSelector{Selector: r.GatewayPodSelector}); err != nil {
		logger.V(1).Info("could not list gateway pods to check --hangar-gateway-selector", "error", err.Error())
		return
	}
	if len(pods.Items) == 0 {
		logger.Error(nil, "--hangar-gateway-selector matches no pod: L7 policies will NOT be re-delivered "+
			"when the gateway restarts; set it to the gateway pods' labels",
			"selector", r.GatewayPodSelector.String())
	}
}
