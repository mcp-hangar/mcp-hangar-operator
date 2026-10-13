package controller

import (
	"context"
	"fmt"
	"os"
	"strings"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
	"github.com/mcp-hangar/operator/pkg/hangar"
	"github.com/mcp-hangar/operator/pkg/metrics"
)

// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;create;update;delete

const (
	// claimPrefix names the Lease that claims a server name in core.
	claimPrefix = "mcp-server-"
	// claimEndpointAnnotation is the endpoint the server was registered with.
	// Core cannot change an endpoint and does not report it back.
	claimEndpointAnnotation = "mcp-hangar.io/registered-endpoint"
	// claimStartedAnnotation is "true" once core has started the registered
	// server, which is the first evidence that the endpoint answers.
	claimStartedAnnotation = "mcp-hangar.io/started"
)

// RegistrationClaims records which MCPServer registered each server name in
// core (#164), as one Lease per name in the operator's own namespace.
//
// Core keys servers by name across the whole cluster, and an MCPServer is
// namespaced, so two CRs can name the same server. The claim is what decides
// whose it is: creating the Lease is atomic, so of two CRs racing for one name
// exactly one wins, and it lives where a namespace's own users cannot write,
// so marking a CR cannot make another namespace's server its to delete or to
// re-point. An annotation on the CR, which #202 used, could do both.
type RegistrationClaims struct {
	// Client writes the claims. Reader reads them, and is meant to be the
	// manager's API reader: through the cache, the first read would start a
	// watch on every Lease in the cluster.
	Client    client.Client
	Reader    client.Reader
	Namespace string
}

// RegistrationNamespace is the namespace the operator runs in, where it keeps
// its claims: POD_NAMESPACE, set by the chart, or the service account's own
// namespace file. Empty when neither is there, and then nothing is registered.
func RegistrationNamespace() string {
	if ns := strings.TrimSpace(os.Getenv("POD_NAMESPACE")); ns != "" {
		return ns
	}
	if b, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace"); err == nil {
		return strings.TrimSpace(string(b))
	}
	return ""
}

func claimHolder(mcpServer *mcpv1alpha2.MCPServer) string {
	return fmt.Sprintf("%s/%s/%s", mcpServer.Namespace, mcpServer.Name, mcpServer.UID)
}

func (c *RegistrationClaims) key(name string) types.NamespacedName {
	return types.NamespacedName{Namespace: c.Namespace, Name: claimPrefix + name}
}

// get returns the claim on a server name, or nil when nothing claims it.
func (c *RegistrationClaims) get(ctx context.Context, name string) (*coordinationv1.Lease, error) {
	lease := &coordinationv1.Lease{}
	if err := c.Reader.Get(ctx, c.key(name), lease); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return lease, nil
}

// take claims the CR's server name. It returns the claim when this CR holds
// it, and nil when another does.
func (c *RegistrationClaims) take(ctx context.Context, mcpServer *mcpv1alpha2.MCPServer) (*coordinationv1.Lease, error) {
	holder := claimHolder(mcpServer)
	key := c.key(mcpServer.Name)
	lease := &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace,
			Labels:      map[string]string{"app.kubernetes.io/managed-by": "mcp-hangar-operator"},
			Annotations: map[string]string{claimEndpointAnnotation: mcpServer.Spec.Endpoint}},
		Spec: coordinationv1.LeaseSpec{HolderIdentity: &holder},
	}
	err := c.Client.Create(ctx, lease)
	if apierrors.IsAlreadyExists(err) {
		existing, getErr := c.get(ctx, mcpServer.Name)
		if getErr != nil || existing == nil || !holds(existing, mcpServer) {
			return nil, getErr
		}
		return existing, nil
	}
	if err != nil {
		return nil, err
	}
	return lease, nil
}

func holds(lease *coordinationv1.Lease, mcpServer *mcpv1alpha2.MCPServer) bool {
	return lease != nil && lease.Spec.HolderIdentity != nil && *lease.Spec.HolderIdentity == claimHolder(mcpServer)
}

func (c *RegistrationClaims) annotate(ctx context.Context, lease *coordinationv1.Lease, kv map[string]string) error {
	if lease.Annotations == nil {
		lease.Annotations = map[string]string{}
	}
	for k, v := range kv {
		lease.Annotations[k] = v
	}
	return c.Client.Update(ctx, lease)
}

func (c *RegistrationClaims) release(ctx context.Context, lease *coordinationv1.Lease) error {
	if err := c.Client.Delete(ctx, lease); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

// reconcileRegistration puts a remote server that core does not hold into
// core, under the CR's name (#164). done reports that this reconcile has
// written its status and the caller returns result and err as they are;
// otherwise the caller goes on to read core's health as for any server.
//
// A CR naming a server core holds without a claim -- one core's config
// declares, say -- stays a mirror: nothing registers it, and deleting the CR
// leaves it alone (#202).
func (r *MCPServerReconciler) reconcileRegistration(ctx context.Context, mcpServer *mcpv1alpha2.MCPServer) (done bool, result ctrl.Result, err error) {
	done, result, err = r.registration(ctx, mcpServer)
	if done && err == nil && result.IsZero() {
		result = registrationRequeue(mcpServer)
	}
	return done, result, err
}

func (r *MCPServerReconciler) registration(ctx context.Context, mcpServer *mcpv1alpha2.MCPServer) (bool, ctrl.Result, error) {
	if r.Claims == nil {
		return false, ctrl.Result{}, nil
	}
	claim, err := r.Claims.get(ctx, mcpServer.Name)
	if err != nil {
		return true, ctrl.Result{}, err
	}
	if claim != nil && !holds(claim, mcpServer) {
		return true, ctrl.Result{}, r.writeRegistrationStatus(ctx, mcpServer, ReasonNameTaken,
			fmt.Sprintf("core's server name %q is registered for MCPServer %s; core keys servers by name across "+
				"the cluster, so rename this one", mcpServer.Name, holderName(claim)))
	}

	// Core cannot change an endpoint: a changed spec.endpoint is removed and
	// registered again. Deregistered first and the claim moved second, so a
	// crash between the two repeats the delete, which core answers 404.
	if claim != nil && claim.Annotations[claimEndpointAnnotation] != mcpServer.Spec.Endpoint {
		if err := r.HangarClient.DeregisterMCPServer(ctx, mcpServer.Name, mcpServer.Namespace); err != nil {
			return true, ctrl.Result{}, err
		}
		r.Recorder.Eventf(mcpServer, nil, corev1.EventTypeNormal, ReasonUpdated, ActionReconcile,
			"Removed %q from core to register it again: its endpoint changed from %s to %s",
			mcpServer.Name, claim.Annotations[claimEndpointAnnotation], mcpServer.Spec.Endpoint)
		if err := r.Claims.annotate(ctx, claim, map[string]string{
			claimEndpointAnnotation: mcpServer.Spec.Endpoint, claimStartedAnnotation: "false"}); err != nil {
			return true, ctrl.Result{}, err
		}
	}

	_, healthErr := r.HangarClient.GetMCPServerHealth(ctx, mcpServer.Name, mcpServer.Namespace)
	switch {
	case hangar.IsNotRegistered(healthErr):
		return r.register(ctx, mcpServer, claim)
	case healthErr != nil || claim == nil:
		// Core could not be asked, or holds a server nobody claimed: the
		// health read that follows reports either.
		return false, ctrl.Result{}, nil //nolint:nilerr // the caller's own health read reports it
	case claim.Annotations[claimStartedAnnotation] != "true":
		return r.start(ctx, mcpServer, claim)
	}
	return false, ctrl.Result{}, nil
}

// holderName is the namespace/name of the MCPServer holding a claim.
func holderName(lease *coordinationv1.Lease) string {
	if lease.Spec.HolderIdentity == nil {
		return "(unknown)"
	}
	h := *lease.Spec.HolderIdentity
	if i := strings.LastIndex(h, "/"); i > 0 {
		return h[:i]
	}
	return h
}

// register claims the name, then asks core. Claimed and not registered is
// safe: the next reconcile registers it, and a delete in between gets 404 from
// core. Registered and not claimed would leave a server in core that deleting
// the CR never removes.
func (r *MCPServerReconciler) register(ctx context.Context, mcpServer *mcpv1alpha2.MCPServer, claim *coordinationv1.Lease) (bool, ctrl.Result, error) {
	if claim == nil {
		var err error
		if claim, err = r.Claims.take(ctx, mcpServer); err != nil {
			return true, ctrl.Result{}, err
		}
		if claim == nil {
			// Lost the race to another CR of the same name; the next
			// reconcile reports which.
			return true, ctrl.Result{RequeueAfter: errorRequeueAfter}, nil
		}
	}

	endpoint := mcpServer.Spec.Endpoint
	err := r.HangarClient.RegisterRemoteMCPServer(ctx, mcpServer.Name, endpoint)
	switch {
	case err == nil:
		r.Recorder.Eventf(mcpServer, nil, corev1.EventTypeNormal, ReasonRegistered, ActionReconcile,
			"Registered %q with core at %s", mcpServer.Name, endpoint)
		return r.start(ctx, mcpServer, claim)
	case hangar.IsAlreadyRegistered(err):
		// Core gained the name between its 404 and this POST, from its config
		// or another API caller. Not this operator's: give the claim back.
		if relErr := r.Claims.release(ctx, claim); relErr != nil {
			return true, ctrl.Result{}, relErr
		}
		return true, ctrl.Result{}, r.writeRegistrationStatus(ctx, mcpServer, ReasonNameTaken,
			fmt.Sprintf("core already holds a server named %q that this operator did not register", mcpServer.Name))
	case hangar.IsEndpointNotTrusted(err):
		return true, ctrl.Result{}, r.writeRegistrationStatus(ctx, mcpServer, ReasonEndpointNotTrusted,
			fmt.Sprintf("core refused %s as a private address it does not trust; add its network to "+
				"ssrf.trusted_networks in core's config (core 2.27.0 or newer)", endpoint))
	case hangar.IsForbidden(err):
		return true, ctrl.Result{}, r.writeRegistrationStatus(ctx, mcpServer, ReasonRegistrationForbidden,
			forbiddenMessage)
	}
	return true, ctrl.Result{}, r.writeRegistrationStatus(ctx, mcpServer, ReasonRegistrationFailed, err.Error())
}

// forbiddenMessage: core took the operator's key and refused its role.
// provider-admin, the role the docs give an operator key, is one such role.
const forbiddenMessage = "core refused the operator's API key: registering a server needs mcp_servers:write, " +
	"starting and deregistering it mcp_servers:lifecycle, which provider-admin does not have"

// start has core start a server this operator registered. Core reports a
// server nobody has called as healthy, so until a start succeeds nothing has
// shown that the endpoint answers -- a mistyped Service name included -- and
// the CR does not claim Ready.
func (r *MCPServerReconciler) start(ctx context.Context, mcpServer *mcpv1alpha2.MCPServer, claim *coordinationv1.Lease) (bool, ctrl.Result, error) {
	err := r.HangarClient.StartMCPServer(ctx, mcpServer.Name)
	switch {
	case err == nil:
		if err := r.Claims.annotate(ctx, claim, map[string]string{claimStartedAnnotation: "true"}); err != nil {
			return true, ctrl.Result{}, err
		}
		return false, ctrl.Result{}, nil
	case hangar.IsForbidden(err):
		return true, ctrl.Result{}, r.writeRegistrationStatus(ctx, mcpServer, ReasonRegistrationForbidden, forbiddenMessage)
	}
	return true, ctrl.Result{}, r.writeRegistrationStatus(ctx, mcpServer, ReasonStartFailed,
		fmt.Sprintf("registered with core, which could not start it at %s: %v", mcpServer.Spec.Endpoint, err))
}

// writeRegistrationStatus reports a registration that did not complete as
// Degraded with reason. Each reason but StartFailed is a setting to change, in
// core or on the CR, so it is re-checked at the steady cadence with one Warning
// on the transition; a failed start is the upstream's and is retried soon.
func (r *MCPServerReconciler) writeRegistrationStatus(ctx context.Context, mcpServer *mcpv1alpha2.MCPServer, reason, msg string) error {
	mcpServer.Status.State = mcpv1alpha2.MCPServerStateDegraded
	mcpServer.Status.Endpoint = mcpServer.Spec.Endpoint
	if conditionChanges(mcpServer.Status.Conditions, ConditionDegraded, metav1.ConditionTrue, reason) {
		r.Recorder.Eventf(mcpServer, nil, corev1.EventTypeWarning, reason, ActionReconcile, "%s", msg)
	}
	setServerCondition(mcpServer, ConditionDegraded, metav1.ConditionTrue, reason, msg)
	metrics.SetMCPServerState(mcpServer.Namespace, mcpServer.Name, string(mcpServer.Status.State))
	return writeStatus(ctx, r.Client, mcpServer)
}

// registrationRequeue is how soon a CR whose registration did not complete is
// looked at again.
func registrationRequeue(mcpServer *mcpv1alpha2.MCPServer) ctrl.Result {
	if c := meta.FindStatusCondition(mcpServer.Status.Conditions, ConditionDegraded); c != nil && c.Reason == ReasonStartFailed {
		return ctrl.Result{RequeueAfter: errorRequeueAfter}
	}
	return ctrl.Result{RequeueAfter: readyRequeueAfter}
}

// deregisterOwned removes from core what this operator put there. A
// container-mode server exists in core because of the pod the operator ran; a
// remote one only if this CR holds the claim on its name.
func (r *MCPServerReconciler) deregisterOwned(ctx context.Context, mcpServer *mcpv1alpha2.MCPServer) error {
	logger := log.FromContext(ctx)
	if mcpServer.IsContainerMode() {
		if err := r.HangarClient.DeregisterMCPServer(ctx, mcpServer.Name, mcpServer.Namespace); err != nil {
			logger.Error(err, "Failed to deregister provider from Hangar")
		}
		return nil
	}
	var claim *coordinationv1.Lease
	if r.Claims != nil {
		var err error
		if claim, err = r.Claims.get(ctx, mcpServer.Name); err != nil {
			return err
		}
	}
	if !holds(claim, mcpServer) {
		r.Recorder.Eventf(mcpServer, nil, corev1.EventTypeNormal, ReasonDeregistrationSkipped, ActionReconcile,
			"Left %q in core: this MCPServer did not register it, so it only mirrored a server declared elsewhere",
			mcpServer.Name)
		return nil
	}
	if err := r.HangarClient.DeregisterMCPServer(ctx, mcpServer.Name, mcpServer.Namespace); err != nil {
		// Not retried: a CR whose delete waits on core -- unreachable, or a
		// key whose role cannot delete -- would never go. The server is left
		// in core, said so once, and the claim goes with the CR.
		logger.Error(err, "Failed to deregister provider from Hangar")
		r.Recorder.Eventf(mcpServer, nil, corev1.EventTypeWarning, ReasonFailed, ActionReconcile,
			"Could not remove %q from core; it stays there until removed by hand: %v", mcpServer.Name, err)
	}
	return r.Claims.release(ctx, claim)
}
