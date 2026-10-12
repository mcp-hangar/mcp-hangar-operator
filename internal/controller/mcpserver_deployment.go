package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
	"github.com/mcp-hangar/operator/pkg/metrics"
	"github.com/mcp-hangar/operator/pkg/provider"
)

// annotationDeploymentHash records, on the Deployment, a hash of the spec the
// operator last applied. Comparing the live spec instead would never match:
// the API server defaults a dozen fields the builder leaves empty, and every
// reconcile would write and announce a change that is not one.
const annotationDeploymentHash = "mcp-hangar.io/deployment-hash"

// ReasonDeploymentNameTaken is the Ready reason when a Deployment of the
// server's name exists and something else controls it.
const ReasonDeploymentNameTaken = "DeploymentNameTaken"

// stuckWaitingReasons are container waiting states that mean the server is
// not coming up on its own, as opposed to ContainerCreating.
var stuckWaitingReasons = map[string]bool{
	"CrashLoopBackOff":           true,
	"ImagePullBackOff":           true,
	"ErrImagePull":               true,
	"InvalidImageName":           true,
	"CreateContainerConfigError": true,
	"CreateContainerError":       true,
}

// reconcileDeployment runs a container-mode server as a Deployment (#221):
// apply the Deployment, retire a pod an older operator created directly, and
// report the server from the Deployment's pod.
func (r *MCPServerReconciler) reconcileDeployment(ctx context.Context, mcpServer *mcpv1alpha2.MCPServer) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	desired, err := provider.BuildDeploymentForMCPServer(mcpServer)
	if err != nil {
		logger.Error(err, "Failed to build Deployment")
		setServerCondition(mcpServer, ConditionReady, metav1.ConditionFalse, "PodBuildFailed", err.Error())
		if err := writeStatus(ctx, r.Client, mcpServer); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: errorRequeueAfter}, nil
	}
	if err := controllerutil.SetControllerReference(mcpServer, desired, r.Scheme); err != nil {
		return ctrl.Result{}, err
	}
	hash, err := deploymentHash(desired)
	if err != nil {
		return ctrl.Result{}, err
	}
	desired.Annotations[annotationDeploymentHash] = hash

	// Before the Deployment's pod can start: two pods under one
	// mcp-hangar.io/name would be a duplicate server to core's discovery.
	if err := r.removeLegacyPod(ctx, mcpServer); err != nil {
		return ctrl.Result{}, err
	}

	existing := &appsv1.Deployment{}
	err = r.Get(ctx, client.ObjectKeyFromObject(desired), existing)
	switch {
	case errors.IsNotFound(err):
		if err := applyOwned(ctx, r.Client, desired); err != nil {
			return r.deploymentWriteFailed(ctx, mcpServer, err)
		}
		r.Recorder.Eventf(mcpServer, nil, corev1.EventTypeNormal, ReasonStarting, ActionReconcile,
			"Created Deployment %s", desired.Name)
		mcpServer.Status.ConsecutiveFailures = 0
		now := metav1.Now()
		mcpServer.Status.LastStartedAt = &now
	case err != nil:
		return ctrl.Result{}, err
	case !metav1.IsControlledBy(existing, mcpServer):
		msg := fmt.Sprintf("Deployment %s exists and is not controlled by this MCPServer; not overwriting it", existing.Name)
		if conditionChanges(mcpServer.Status.Conditions, ConditionReady, metav1.ConditionFalse, ReasonDeploymentNameTaken) {
			r.Recorder.Eventf(mcpServer, nil, corev1.EventTypeWarning, ReasonDeploymentNameTaken, ActionReconcile, "%s", msg)
		}
		mcpServer.Status.State = mcpv1alpha2.MCPServerStateDead
		setServerCondition(mcpServer, ConditionReady, metav1.ConditionFalse, ReasonDeploymentNameTaken, msg)
		if err := writeStatus(ctx, r.Client, mcpServer); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: coldRequeueAfter}, nil
	case existing.Annotations[annotationDeploymentHash] != hash:
		if err := applyOwned(ctx, r.Client, desired); err != nil {
			return r.deploymentWriteFailed(ctx, mcpServer, err)
		}
		// A new spec is a new attempt: the old one's failures do not count.
		mcpServer.Status.ConsecutiveFailures = 0
		setServerCondition(mcpServer, ConditionProgressing, metav1.ConditionTrue, "SpecChanged",
			"Provider spec changed, rolling out the Deployment")
		r.Recorder.Eventf(mcpServer, nil, corev1.EventTypeNormal, ReasonUpdated, ActionReconcile,
			"Spec changed, rolling out Deployment %s", desired.Name)
		metrics.MCPServerRestarts.WithLabelValues(mcpServer.Namespace, mcpServer.Name).Inc()
	}

	if mcpServer.IsCold() {
		return r.reportCold(ctx, mcpServer)
	}

	pod, err := r.currentPod(ctx, mcpServer)
	if err != nil {
		return ctrl.Result{}, err
	}
	if pod == nil {
		mcpServer.Status.State = mcpv1alpha2.MCPServerStateInitializing
		mcpServer.Status.Replicas = 1
		mcpServer.Status.ReadyReplicas = 0
		setServerCondition(mcpServer, ConditionProgressing, metav1.ConditionTrue, "PodCreating",
			"Waiting for the Deployment to create the pod")
		metrics.SetMCPServerState(mcpServer.Namespace, mcpServer.Name, string(mcpv1alpha2.MCPServerStateInitializing))
		if err := writeStatus(ctx, r.Client, mcpServer); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: defaultRequeueAfter}, nil
	}
	return r.syncPodStatus(ctx, mcpServer, pod)
}

// deploymentWriteFailed reports a Deployment the API server refused.
func (r *MCPServerReconciler) deploymentWriteFailed(ctx context.Context, mcpServer *mcpv1alpha2.MCPServer, err error) (ctrl.Result, error) {
	log.FromContext(ctx).Error(err, "Failed to apply Deployment")
	setServerCondition(mcpServer, ConditionReady, metav1.ConditionFalse, "DeploymentApplyFailed", err.Error())
	mcpServer.Status.State = mcpv1alpha2.MCPServerStateDead
	if werr := writeStatus(ctx, r.Client, mcpServer); werr != nil {
		return ctrl.Result{}, werr
	}
	r.Recorder.Eventf(mcpServer, nil, corev1.EventTypeWarning, ReasonFailed, ActionReconcile,
		"Failed to apply Deployment: %v", err)
	return ctrl.Result{RequeueAfter: errorRequeueAfter}, nil
}

// reportCold records a server switched off with replicas: 0. The Deployment
// stays, scaled to zero.
func (r *MCPServerReconciler) reportCold(ctx context.Context, mcpServer *mcpv1alpha2.MCPServer) (ctrl.Result, error) {
	mcpServer.Status.State = mcpv1alpha2.MCPServerStateCold
	mcpServer.Status.Replicas = 0
	mcpServer.Status.ReadyReplicas = 0
	mcpServer.Status.AvailableReplicas = 0
	mcpServer.Status.PodName = ""
	setServerCondition(mcpServer, ConditionReady, metav1.ConditionFalse, "Cold", "spec.replicas is 0, so no provider pod runs; set it to 1 to start one")
	setServerCondition(mcpServer, ConditionAvailable, metav1.ConditionFalse, "Cold", "No replicas requested")
	if err := writeStatus(ctx, r.Client, mcpServer); err != nil {
		return ctrl.Result{}, err
	}
	metrics.SetMCPServerState(mcpServer.Namespace, mcpServer.Name, string(mcpv1alpha2.MCPServerStateCold))
	return ctrl.Result{RequeueAfter: coldRequeueAfter}, nil
}

// removeLegacyPod deletes the fixed-name pod an operator before #221 created
// directly, owned by the MCPServer itself. A pod the Deployment made is owned
// by its ReplicaSet and never matches.
func (r *MCPServerReconciler) removeLegacyPod(ctx context.Context, mcpServer *mcpv1alpha2.MCPServer) error {
	pod := &corev1.Pod{}
	err := r.Get(ctx, types.NamespacedName{Name: mcpServer.GetPodName(), Namespace: mcpServer.Namespace}, pod)
	if errors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !metav1.IsControlledBy(pod, mcpServer) || pod.DeletionTimestamp != nil {
		return nil
	}
	if err := r.Delete(ctx, pod); err != nil && !errors.IsNotFound(err) {
		return err
	}
	r.Recorder.Eventf(mcpServer, nil, corev1.EventTypeNormal, "LegacyPodRemoved", ActionReconcile,
		"Removed pod %s; the server now runs as Deployment %s", pod.Name, provider.DeploymentName(mcpServer))
	return nil
}

// currentPod is the newest live pod of the server's Deployment, or nil.
func (r *MCPServerReconciler) currentPod(ctx context.Context, mcpServer *mcpv1alpha2.MCPServer) (*corev1.Pod, error) {
	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.InNamespace(mcpServer.Namespace),
		client.MatchingLabels(provider.DeploymentSelector(mcpServer))); err != nil {
		return nil, err
	}
	var newest *corev1.Pod
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.DeletionTimestamp != nil || metav1.IsControlledBy(p, mcpServer) {
			continue // terminating, or the legacy pod on its way out
		}
		if newest == nil || newest.CreationTimestamp.Before(&p.CreationTimestamp) {
			newest = p
		}
	}
	return newest, nil
}

// containerStuck reports a container that is not coming up on its own, with
// its restart count.
func containerStuck(pod *corev1.Pod) (reason, msg string, restarts int32, stuck bool) {
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Waiting != nil && stuckWaitingReasons[cs.State.Waiting.Reason] {
			msg := cs.State.Waiting.Message
			if t := cs.LastTerminationState.Terminated; t != nil && msg == "" {
				msg = fmt.Sprintf("last exit %d (%s)", t.ExitCode, t.Reason)
			}
			return cs.State.Waiting.Reason, msg, cs.RestartCount, true
		}
	}
	return "", "", 0, false
}

// deploymentHash is the hash of what the operator applies, recorded so that
// only a real change is written.
func deploymentHash(d *appsv1.Deployment) (string, error) {
	b, err := json.Marshal(struct {
		Labels map[string]string     `json:"labels"`
		Spec   appsv1.DeploymentSpec `json:"spec"`
	}{d.Labels, d.Spec})
	if err != nil {
		return "", fmt.Errorf("hash Deployment: %w", err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8]), nil
}

// providerPodToServer maps a provider pod to its MCPServer by the label the
// builder stamps; pods of anything else map to nothing.
func providerPodToServer(_ context.Context, obj client.Object) []reconcile.Request {
	l := obj.GetLabels()
	name := l[provider.LabelProvider]
	if name == "" || l[provider.LabelManagedBy] != provider.DefaultManagerName {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: obj.GetNamespace(), Name: name}}}
}
