package provider

import (
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
)

// deploymentRevisionHistory keeps one ReplicaSet back for `kubectl rollout
// undo`; the default of ten is ten idle objects per server.
const deploymentRevisionHistory = int32(2)

// DeploymentName is the name of the Deployment that runs a container-mode
// server. It is the name the fixed pod used to have, so the two never coexist
// under different names in a listing (#221).
func DeploymentName(provider *mcpv1alpha2.MCPServer) string {
	return provider.GetPodName()
}

// DeploymentSelector is the label selector of a server's Deployment. A
// Deployment's selector cannot change after creation, so it is the smallest
// stable set: the server's name and the manager. The UID is deliberately not
// in it: a CR deleted and recreated under the same name gets a new Deployment
// through garbage collection anyway.
func DeploymentSelector(provider *mcpv1alpha2.MCPServer) map[string]string {
	return map[string]string{
		LabelProvider:  provider.Name,
		LabelManagedBy: DefaultManagerName,
	}
}

// BuildDeploymentForMCPServer builds the Deployment that runs a container-mode
// server (#221). The pod template is exactly the pod BuildPodForMCPServer
// builds -- labels, discovery annotations, security defaults -- with the one
// change a Deployment requires: RestartPolicy Always, so kubelet restarts a
// crashed server with its own backoff instead of the operator deleting and
// recreating a fixed-name pod.
//
// Replicas follow spec.replicas, which the CRD bounds to 0 or 1: core
// registers one server per mcp-hangar.io/name (#259). For the same reason the
// strategy is Recreate: a surge would run two pods under one name for a
// moment, and core's discovery would see a duplicate.
func BuildDeploymentForMCPServer(provider *mcpv1alpha2.MCPServer) (*appsv1.Deployment, error) {
	pod, err := BuildPodForMCPServer(provider)
	if err != nil {
		return nil, err
	}
	podSpec := pod.Spec
	podSpec.RestartPolicy = corev1.RestartPolicyAlways

	replicas := provider.GetReplicas()
	history := deploymentRevisionHistory
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      DeploymentName(provider),
			Namespace: provider.Namespace,
			Labels:    buildLabels(provider),
			Annotations: map[string]string{
				AnnotationGeneration: pod.Annotations[AnnotationGeneration],
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas:             &replicas,
			Selector:             &metav1.LabelSelector{MatchLabels: DeploymentSelector(provider)},
			Strategy:             appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType},
			RevisionHistoryLimit: &history,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      pod.Labels,
					Annotations: pod.Annotations,
				},
				Spec: podSpec,
			},
		},
	}, nil
}
