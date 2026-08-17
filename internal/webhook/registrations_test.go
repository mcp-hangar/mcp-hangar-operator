package webhook_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"

	"github.com/mcp-hangar/operator/internal/webhook"
)

// Kinds that deliberately have no admission validator. Every entry needs a
// reason; an undocumented gap is exactly what this test exists to catch.
var noValidatorByDesign = map[string]string{
	// Constraint checking is entirely in the CRD schema + CEL rules
	// (x-kubernetes-validations); there is no cross-field or cross-object
	// invariant an admission webhook would add.
	"MCPEgressPolicy": "CRD schema + CEL only",
}

// TestEveryServedVersionHasAValidator asserts the invariant that produced
// two real bugs when it was implicit:
//   - #12: a write submitted at version X only reaches version X's validator,
//     so every served version needs its own registration;
//   - #137: the wildcard-egress guard lived only in the v1alpha1 validator --
//     once that version stopped being served, nothing enforced the guard
//     while its unit tests stayed green against the dead code path.
//
// For every served version of every CRD this operator ships, there must be
// (a) a registration in webhook.Registrations() (what main.go wires) and
// (b) a rule in config/webhook/manifests.yaml (what the cluster routes),
// unless the kind is documented above as validator-free by design.
func TestEveryServedVersionHasAValidator(t *testing.T) {
	served := servedVersionsByKind(t) // kind -> served versions, from config/crd/bases
	registered := map[string]bool{}   // "Kind/version"
	for _, r := range webhook.Registrations() {
		registered[r.Name] = true
	}
	routed := routedVersionsByResource(t) // plural resource -> apiVersions, from manifests.yaml

	for kind, crd := range served {
		if reason, ok := noValidatorByDesign[kind]; ok {
			t.Logf("%s: no validator by design (%s)", kind, reason)
			continue
		}
		for _, version := range crd.versions {
			key := kind + "/" + version
			assert.True(t, registered[key],
				"%s is served but webhook.Registrations() has no validator for it -- a write at %s bypasses admission (see #12/#137)", key, version)
			assert.Contains(t, routed[crd.plural], version,
				"%s is served but config/webhook/manifests.yaml routes no rule for %s at %s -- run `make manifests` and check the kubebuilder:webhook marker", key, crd.plural, version)
		}
	}

	// The reverse direction: a registration for a version no CRD serves is a
	// validator nothing can reach -- dead code presenting as coverage.
	for name := range registered {
		kind, version := splitReg(name)
		crd, ok := served[kind]
		require.True(t, ok, "registration %s names a kind with no CRD", name)
		assert.Contains(t, crd.versions, version,
			"registration %s targets a version the %s CRD does not serve -- the #137 shape (guard on a dead path)", name, kind)
	}
}

type crdInfo struct {
	plural   string
	versions []string
}

func servedVersionsByKind(t *testing.T) map[string]crdInfo {
	t.Helper()
	base := filepath.Join("..", "..", "config", "crd", "bases")
	entries, err := os.ReadDir(base)
	require.NoError(t, err)
	out := map[string]crdInfo{}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(base, e.Name()))
		require.NoError(t, err)
		crd := &apiextensionsv1.CustomResourceDefinition{}
		require.NoError(t, yaml.Unmarshal(data, crd), e.Name())
		info := crdInfo{plural: crd.Spec.Names.Plural}
		for _, v := range crd.Spec.Versions {
			if v.Served {
				info.versions = append(info.versions, v.Name)
			}
		}
		out[crd.Spec.Names.Kind] = info
	}
	require.NotEmpty(t, out, "no CRDs found under config/crd/bases")
	return out
}

func routedVersionsByResource(t *testing.T) map[string][]string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "config", "webhook", "manifests.yaml"))
	require.NoError(t, err)
	var cfg struct {
		Webhooks []struct {
			Rules []struct {
				APIVersions []string `json:"apiVersions"`
				Resources   []string `json:"resources"`
			} `json:"rules"`
		} `json:"webhooks"`
	}
	require.NoError(t, yaml.Unmarshal(data, &cfg))
	out := map[string][]string{}
	for _, w := range cfg.Webhooks {
		for _, r := range w.Rules {
			for _, res := range r.Resources {
				out[res] = append(out[res], r.APIVersions...)
			}
		}
	}
	return out
}

func splitReg(name string) (kind, version string) {
	for i := range name {
		if name[i] == '/' {
			return name[:i], name[i+1:]
		}
	}
	return name, ""
}
