package webhook

import (
	"context"
	"fmt"
	"net/http"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
	"github.com/mcp-hangar/operator/pkg/networkpolicy"
)

// +kubebuilder:webhook:path=/validate-pod-registration,mutating=false,failurePolicy=fail,sideEffects=None,groups="",resources=pods,verbs=create;update,versions=v1,name=vpod-registration.kb.io,admissionReviewVersions=v1

// PodRegistrationValidator rejects a Pod that claims to be an MCP server -- via
// the mcp-hangar.io/provider=<name> label -- when no MCPServer named <name>
// exists in the Pod's namespace (#50, OWASP MCP09). An unregistered/shadow
// provider pod fails to deploy rather than only being denied egress (the
// phase-1 default-deny, #51). The webhook is scoped by namespaceSelector to
// namespaces opted into enforcement (mcp-hangar.io/enforce-egress=true), so it
// only fires there -- a fail-closed webhook there does not gate other namespaces.
//
// It gates UPDATE as well as CREATE (#189): the per-server allow NetworkPolicy
// selects pods by exactly this label, so a pod admitted unlabelled and
// labelled afterwards would inherit a registered server's egress with no
// admission call at all. Once a pod is admitted its provider label is
// immutable: adding or changing it is denied, whatever the new value names.
// The operator's own pods carry the label from creation (pkg/provider
// buildLabels) and the controller never relabels a pod, so none of its writes
// are denied. Removing the label is allowed -- the pod leaves the server's
// allow-policy, which is the safe direction. Updates that leave the label as it
// was are allowed without a lookup: a finalizer or ownerRef patch on a pod
// whose MCPServer has since been deleted must not wedge the pod.
//
// The rule matches the main resource only; kubelet status patches go through
// the pods/status subresource and never reach this webhook.
type PodRegistrationValidator struct {
	Client  client.Client
	Decoder admission.Decoder
}

// Handle validates a Pod create or update against MCPServer registration.
func (v *PodRegistrationValidator) Handle(ctx context.Context, req admission.Request) admission.Response {
	pod := &corev1.Pod{}
	if err := v.Decoder.Decode(req, pod); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	provider := pod.Labels[networkpolicy.LabelProvider]

	if req.Operation == admissionv1.Update {
		oldPod := &corev1.Pod{}
		if err := v.Decoder.DecodeRaw(req.OldObject, oldPod); err != nil {
			return admission.Errored(http.StatusBadRequest, err)
		}
		return validateProviderLabelUpdate(oldPod.Labels[networkpolicy.LabelProvider], provider, req.Namespace)
	}

	if provider == "" {
		// Not claiming to be an MCP server; not our concern (phase-1 default-deny
		// still limits its egress in an enforced namespace).
		return admission.Allowed("not an MCP-server pod")
	}

	var server mcpv1alpha2.MCPServer
	key := types.NamespacedName{Namespace: req.Namespace, Name: provider}
	if err := v.Client.Get(ctx, key, &server); err != nil {
		if apierrors.IsNotFound(err) {
			return admission.Denied(fmt.Sprintf(
				"pod labelled %s=%q has no registered MCPServer %q in namespace %q; register the server or remove the label (#50)",
				networkpolicy.LabelProvider, provider, provider, req.Namespace))
		}
		return admission.Errored(http.StatusInternalServerError, err)
	}

	return admission.Allowed("registered MCPServer exists")
}

// validateProviderLabelUpdate decides an UPDATE from the old and new values of
// the provider label alone (#189). The label is immutable once the pod is
// admitted, except that it may be removed.
func validateProviderLabelUpdate(oldProvider, newProvider, namespace string) admission.Response {
	switch {
	case oldProvider == newProvider:
		return admission.Allowed("provider label unchanged")
	case newProvider == "":
		return admission.Allowed("provider label removed; pod leaves the server's allow-policy")
	case oldProvider == "":
		return admission.Denied(fmt.Sprintf(
			"pod cannot be labelled %s=%q after admission in namespace %q; the label is set at pod creation and admitted then (#189)",
			networkpolicy.LabelProvider, newProvider, namespace))
	default:
		return admission.Denied(fmt.Sprintf(
			"pod label %s cannot change from %q to %q in namespace %q; the label is immutable once set -- recreate the pod (#189)",
			networkpolicy.LabelProvider, oldProvider, newProvider, namespace))
	}
}
