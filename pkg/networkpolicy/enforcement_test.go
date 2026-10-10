package networkpolicy

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// emptyMapper serves nothing beyond core Kubernetes: the shape of a vcluster
// API server, where the CNI's CRDs live on the host.
func emptyMapper() meta.RESTMapper {
	return meta.NewDefaultRESTMapper(nil)
}

func mapperWith(gk schema.GroupKind, version string) meta.RESTMapper {
	gv := schema.GroupVersion{Group: gk.Group, Version: version}
	m := meta.NewDefaultRESTMapper([]schema.GroupVersion{gv})
	m.Add(gv.WithKind(gk.Kind), meta.RESTScopeNamespace)
	return m
}

func daemonSetReader(t *testing.T, names ...string) client.Reader {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	objs := make([]client.Object, 0, len(names))
	for _, name := range names {
		objs = append(objs, &appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kube-system"},
		})
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
}

type erroringReader struct {
	client.Reader
	calls int
}

func (e *erroringReader) List(context.Context, client.ObjectList, ...client.ListOption) error {
	e.calls++
	return errors.New("daemonsets is forbidden")
}

func TestProbe_CNIPolicyAPIServedHere_Observed(t *testing.T) {
	p := &EnforcementProbe{
		Mapper: mapperWith(schema.GroupKind{Group: "crd.projectcalico.org", Kind: "GlobalNetworkPolicy"}, "v1"),
	}

	signal := p.Signal(context.Background())

	assert.Equal(t, EnforcementObserved, signal.Verdict)
	assert.Contains(t, signal.Source, "Calico")
}

func ciliumCRDMapper() meta.RESTMapper {
	return mapperWith(schema.GroupKind{Group: CiliumGroup, Kind: CiliumNetworkPolicyKind}, CiliumVersion)
}

// Cilium's CRDs survive `cilium uninstall` unless purged. A cluster that moved
// to another CNI, or never ran the agent, still serves them; that is not an
// enforcer (#200).
func TestProbe_CiliumCRDWithoutAgent_NotObserved(t *testing.T) {
	p := &EnforcementProbe{Mapper: ciliumCRDMapper(), Reader: daemonSetReader(t, "coredns")}

	signal := p.Signal(context.Background())

	assert.Equal(t, EnforcementNotObserved, signal.Verdict)
	assert.False(t, signal.CiliumAgent)
	assert.Contains(t, signal.Source, "CiliumNetworkPolicy CRD is served")
}

func TestProbe_CiliumCRDAndAgent_Observed(t *testing.T) {
	p := &EnforcementProbe{Mapper: ciliumCRDMapper(), Reader: daemonSetReader(t, "cilium")}

	signal := p.Signal(context.Background())

	assert.Equal(t, EnforcementObserved, signal.Verdict)
	assert.True(t, signal.CiliumAgent)
	assert.Contains(t, signal.Source, "Cilium (kube-system/cilium)")
}

// Without the DaemonSet grant the CRD cannot be confirmed: doubt, and not Cilium.
func TestProbe_CiliumCRDAgentsUnlistable_Unknown(t *testing.T) {
	p := &EnforcementProbe{Mapper: ciliumCRDMapper(), Reader: &erroringReader{}}

	signal := p.Signal(context.Background())

	assert.Equal(t, EnforcementUnknown, signal.Verdict)
	assert.False(t, signal.CiliumAgent)
	assert.Contains(t, signal.Source, "forbidden")
}

// Calico enforces; the Cilium CRD beside it is left over. Observed, but the
// backstop must not take the Cilium flavor.
func TestProbe_CalicoWithLeftoverCiliumCRD_ObservedNotCilium(t *testing.T) {
	gvs := []schema.GroupVersion{
		{Group: CiliumGroup, Version: CiliumVersion},
		{Group: "crd.projectcalico.org", Version: "v1"},
	}
	m := meta.NewDefaultRESTMapper(gvs)
	m.Add(gvs[0].WithKind(CiliumNetworkPolicyKind), meta.RESTScopeNamespace)
	m.Add(gvs[1].WithKind("GlobalNetworkPolicy"), meta.RESTScopeRoot)
	p := &EnforcementProbe{Mapper: m, Reader: daemonSetReader(t, "calico-node")}

	signal := p.Signal(context.Background())

	assert.Equal(t, EnforcementObserved, signal.Verdict)
	assert.False(t, signal.CiliumAgent)
}

// The version is not part of the question: a cluster that moved Antrea to a
// newer API group version still enforces.
func TestProbe_EnforcerAPIMatchesAnyVersion(t *testing.T) {
	p := &EnforcementProbe{
		Mapper: mapperWith(schema.GroupKind{Group: "crd.antrea.io", Kind: "AntreaAgentInfo"}, "v1beta2"),
	}

	assert.Equal(t, EnforcementObserved, p.Signal(context.Background()).Verdict)
}

// An enforcer with no CRD to recognize it by is still an enforcer. Without this
// second look a working Azure NPM cluster would be told its backstop is inert.
func TestProbe_AgentWithoutCRD_Observed(t *testing.T) {
	p := &EnforcementProbe{Mapper: emptyMapper(), Reader: daemonSetReader(t, "azure-npm")}

	signal := p.Signal(context.Background())

	assert.Equal(t, EnforcementObserved, signal.Verdict)
	assert.Contains(t, signal.Source, "Azure NPM")
}

// The #172 cluster: the operator's API server serves no policy API and runs no
// agent, because both are on the host it cannot see.
func TestProbe_NoAPIAndNoAgent_NotObserved(t *testing.T) {
	p := &EnforcementProbe{Mapper: emptyMapper(), Reader: daemonSetReader(t, "coredns", "kube-proxy")}

	signal := p.Signal(context.Background())

	assert.Equal(t, EnforcementNotObserved, signal.Verdict)
	assert.Contains(t, signal.Source, "no reader")
}

// A question the operator was not allowed to ask is doubt, not a fault.
func TestProbe_ListForbidden_Unknown(t *testing.T) {
	p := &EnforcementProbe{Mapper: emptyMapper(), Reader: &erroringReader{}}

	signal := p.Signal(context.Background())

	assert.Equal(t, EnforcementUnknown, signal.Verdict)
	assert.Contains(t, signal.Source, "forbidden")
}

func TestProbe_NilProbe_Unknown(t *testing.T) {
	var p *EnforcementProbe

	assert.Equal(t, EnforcementUnknown, p.Signal(context.Background()).Verdict)
}

// The flag replaces the verdict, but not the look at which CNI runs: it asserts
// that policy is enforced, not that Cilium enforces it (#200).
func TestProbe_Override_ReplacesTheVerdictOnly(t *testing.T) {
	p := &EnforcementProbe{Mapper: emptyMapper(), Reader: &erroringReader{}, Override: EnforcementObserved}

	signal := p.Signal(context.Background())

	assert.Equal(t, EnforcementObserved, signal.Verdict)
	assert.Contains(t, signal.Source, "--networkpolicy-enforcement")
	assert.False(t, signal.CiliumAgent)

	withCilium := &EnforcementProbe{
		Mapper: ciliumCRDMapper(), Reader: daemonSetReader(t, "cilium"), Override: EnforcementNotObserved,
	}
	signal = withCilium.Signal(context.Background())
	assert.Equal(t, EnforcementNotObserved, signal.Verdict)
	assert.True(t, signal.CiliumAgent)
}

// Twelve reconciles an hour must not be twelve cluster-wide DaemonSet lists.
func TestProbe_CachesForTheTTL(t *testing.T) {
	reader := &erroringReader{}
	now := time.Now()
	p := &EnforcementProbe{
		Mapper: emptyMapper(),
		Reader: reader,
		TTL:    5 * time.Minute,
		Now:    func() time.Time { return now },
	}

	for range 3 {
		p.Signal(context.Background())
	}
	assert.Equal(t, 1, reader.calls)

	now = now.Add(6 * time.Minute)
	p.Signal(context.Background())
	assert.Equal(t, 2, reader.calls, "the verdict must expire, so a newly installed CNI is seen")
}
