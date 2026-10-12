package provider

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/utils/ptr"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
)

func deploymentServer() *mcpv1alpha2.MCPServer {
	return &mcpv1alpha2.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "srv", Namespace: "ns", UID: "uid-1", Generation: 3},
		Spec: mcpv1alpha2.MCPServerSpec{
			Mode:  mcpv1alpha2.MCPServerModeContainer,
			Image: "example.com/srv@sha256:" + "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90",
		},
	}
}

// The template is the pod the operator used to create, so everything core
// and the security defaults read is unchanged; only RestartPolicy differs.
func TestBuildDeployment_TemplateIsThePod(t *testing.T) {
	srv := deploymentServer()
	pod, err := BuildPodForMCPServer(srv)
	require.NoError(t, err)

	dep, err := BuildDeploymentForMCPServer(srv)
	require.NoError(t, err)

	assert.Equal(t, "mcp-provider-srv", dep.Name)
	assert.Equal(t, "ns", dep.Namespace)
	assert.Equal(t, pod.Labels, dep.Spec.Template.Labels)
	assert.Equal(t, pod.Annotations, dep.Spec.Template.Annotations, "the discovery annotations core reads")
	assert.Equal(t, "true", dep.Spec.Template.Annotations[AnnotationDiscoveryEnabled])
	assert.Equal(t, corev1.RestartPolicyAlways, dep.Spec.Template.Spec.RestartPolicy)
	want := pod.Spec
	want.RestartPolicy = corev1.RestartPolicyAlways
	assert.Equal(t, want, dep.Spec.Template.Spec)
	assert.Equal(t, "3", dep.Annotations[AnnotationGeneration])
}

// A Deployment whose selector does not match its own template is refused by
// the API server; the selector must also be immutable-safe (no UID).
func TestBuildDeployment_SelectorMatchesTheTemplate(t *testing.T) {
	dep, err := BuildDeploymentForMCPServer(deploymentServer())
	require.NoError(t, err)

	sel, err := metav1.LabelSelectorAsSelector(dep.Spec.Selector)
	require.NoError(t, err)
	assert.True(t, sel.Matches(labels.Set(dep.Spec.Template.Labels)))
	assert.NotContains(t, dep.Spec.Selector.MatchLabels, LabelProviderUID)
}

// Replicas follow spec.replicas (0 or 1, #259), defaulting to 1.
func TestBuildDeployment_Replicas(t *testing.T) {
	srv := deploymentServer()
	dep, err := BuildDeploymentForMCPServer(srv)
	require.NoError(t, err)
	assert.Equal(t, int32(1), *dep.Spec.Replicas)

	srv.Spec.Replicas = ptr.To(int32(0))
	dep, err = BuildDeploymentForMCPServer(srv)
	require.NoError(t, err)
	assert.Equal(t, int32(0), *dep.Spec.Replicas)
}

// Recreate, never a surge: two pods under one mcp-hangar.io/name would be a
// duplicate server to core's discovery.
func TestBuildDeployment_RecreatesRatherThanSurges(t *testing.T) {
	dep, err := BuildDeploymentForMCPServer(deploymentServer())
	require.NoError(t, err)
	assert.Equal(t, appsv1.RecreateDeploymentStrategyType, dep.Spec.Strategy.Type)
	assert.Nil(t, dep.Spec.Strategy.RollingUpdate)
}

func TestBuildDeployment_NoImage(t *testing.T) {
	srv := deploymentServer()
	srv.Spec.Image = ""
	_, err := BuildDeploymentForMCPServer(srv)
	assert.Error(t, err)
}
