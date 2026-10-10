package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	"sigs.k8s.io/yaml"
)

// At terminationGracePeriodSeconds equal to the manager's graceful-shutdown
// timeout, the kubelet kills the process the moment it stops draining and the
// leader lease release can be lost (#215). The kustomize manifest must leave
// room after the default --graceful-shutdown-timeout.
func TestManagerManifest_GracePeriodOutlastsTheDrain(t *testing.T) {
	data, err := os.ReadFile("../../config/manager/manager.yaml")
	require.NoError(t, err)
	var d appsv1.Deployment
	for _, doc := range strings.Split(string(data), "\n---") {
		var probe appsv1.Deployment
		require.NoError(t, yaml.Unmarshal([]byte(doc), &probe))
		if probe.Kind == "Deployment" {
			d = probe
		}
	}
	require.NotNil(t, d.Spec.Template.Spec.TerminationGracePeriodSeconds, "no Deployment in manager.yaml")

	grace := time.Duration(*d.Spec.Template.Spec.TerminationGracePeriodSeconds) * time.Second
	require.Greater(t, grace, defaultGracefulShutdownTimeout)
}
