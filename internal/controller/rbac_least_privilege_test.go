package controller

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"
)

func loadRole(t *testing.T, name string) rbacv1.ClusterRole {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "config", "rbac", name))
	require.NoError(t, err)
	var role rbacv1.ClusterRole // Role and ClusterRole share the rules shape
	require.NoError(t, yaml.Unmarshal(data, &role))
	return role
}

// Nothing in the operator reads a Secret or a ServiceAccount: a pod that
// references either is resolved by the kubelet, not by us. The grants were
// left over from an earlier design and gave the operator cluster-wide read on
// every Secret (#194). The envtest suite runs as admin, so only this keeps
// them from coming back with a stray marker.
func TestRBAC_ManagerRoleReadsNoSecretsOrServiceAccounts(t *testing.T) {
	for _, rule := range loadRole(t, "role.yaml").Rules {
		assert.NotContains(t, rule.Resources, "secrets")
		assert.NotContains(t, rule.Resources, "serviceaccounts")
	}
}

// The leader lock is a Lease (controller-runtime's default); a ConfigMap
// grant there is the pre-leases lock and full CRUD on ConfigMaps for nothing.
func TestRBAC_LeaderElectionRoleIsLeasesAndEvents(t *testing.T) {
	var resources []string
	for _, rule := range loadRole(t, "leader_election_role.yaml").Rules {
		resources = append(resources, rule.Resources...)
	}
	assert.ElementsMatch(t, []string{"leases", "events"}, resources)
}
