package networkpolicy

import (
	"fmt"
	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
	"github.com/mcp-hangar/operator/pkg/provider"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/validation"
)

const AnnotationProviderIngress = "mcp-hangar.io/provider-ingress"

// ProviderIngressConfig is opt-in. Namespace and pod selectors are ANDed;
// a matching label in an unrelated namespace never grants access.
type ProviderIngressConfig struct {
	Enabled          bool
	GatewayNamespace string
	GatewaySelector  *metav1.LabelSelector
}

func ParseProviderIngressConfig(enabled bool, namespace, selector string) (ProviderIngressConfig, error) {
	c := ProviderIngressConfig{Enabled: enabled, GatewayNamespace: namespace}
	if !enabled && namespace == "" {
		return c, nil
	}
	if errs := validation.IsDNS1123Label(namespace); len(errs) > 0 {
		return c, fmt.Errorf("provider ingress gateway namespace: %v", errs)
	}
	if selector == "" {
		return c, fmt.Errorf("provider ingress requires a non-empty gateway pod selector")
	}
	sel, err := metav1.ParseToLabelSelector(selector)
	if err != nil {
		return c, fmt.Errorf("provider ingress gateway selector: %w", err)
	}
	if len(sel.MatchLabels)+len(sel.MatchExpressions) == 0 {
		return c, fmt.Errorf("provider ingress requires a non-empty gateway pod selector")
	}
	c.GatewaySelector = sel
	return c, nil
}

func ProviderIngressPolicyName(name string) string { return name + "-ingress" }

func BuildProviderIngress(server *mcpv1alpha2.MCPServer, c ProviderIngressConfig) (*networkingv1.NetworkPolicy, error) {
	if c.GatewayNamespace == "" || c.GatewaySelector == nil || len(c.GatewaySelector.MatchLabels)+len(c.GatewaySelector.MatchExpressions) == 0 {
		return nil, fmt.Errorf("provider ingress requires an explicit gateway namespace and non-empty pod selector")
	}
	port := intstr.FromInt32(provider.DefaultDiscoveryPort)
	protocol := corev1.ProtocolTCP
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: ProviderIngressPolicyName(server.Name), Namespace: server.Namespace, Labels: map[string]string{LabelManagedBy: DefaultManagerName, LabelProvider: server.Name}},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{LabelProvider: server.Name}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{{
				From:  []networkingv1.NetworkPolicyPeer{{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": c.GatewayNamespace}}, PodSelector: c.GatewaySelector.DeepCopy()}},
				Ports: []networkingv1.NetworkPolicyPort{{Port: &port, Protocol: &protocol}},
			}},
		},
	}, nil
}
