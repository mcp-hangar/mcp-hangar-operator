package webhook

import (
	"context"
	"fmt"
	"net/http"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	mcpv1alpha1 "github.com/mcp-hangar/operator/api/v1alpha1"
	"github.com/mcp-hangar/operator/pkg/networkpolicy"
)

// +kubebuilder:webhook:path=/validate-pod-registration,mutating=false,failurePolicy=fail,sideEffects=None,groups="",resources=pods,verbs=create,versions=v1,name=vpod-registration.kb.io,admissionReviewVersions=v1

// PodRegistrationValidator rejects a Pod that claims to be an MCP server -- via
// the mcp-hangar.io/provider=<name> label -- when no MCPServer named <name>
// exists in the Pod's namespace (#50, OWASP MCP09). An unregistered/shadow
// provider pod fails to deploy rather than only being denied egress (the
// phase-1 default-deny, #51). The webhook is scoped by namespaceSelector to
// namespaces opted into enforcement (mcp-hangar.io/enforce-egress=true), so it
// only fires there -- a fail-closed webhook there does not gate other namespaces.
type PodRegistrationValidator struct {
	Client  client.Client
	Decoder admission.Decoder
}

// Handle validates a Pod create against MCPServer registration.
func (v *PodRegistrationValidator) Handle(ctx context.Context, req admission.Request) admission.Response {
	pod := &corev1.Pod{}
	if err := v.Decoder.Decode(req, pod); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	provider := pod.Labels[networkpolicy.LabelProvider]
	if provider == "" {
		// Not claiming to be an MCP server; not our concern (phase-1 default-deny
		// still limits its egress in an enforced namespace).
		return admission.Allowed("not an MCP-server pod")
	}

	var server mcpv1alpha1.MCPServer
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
