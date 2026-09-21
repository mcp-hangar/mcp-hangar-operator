package networkpolicy

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// EnforcementVerdict is what a probe could establish about the API server this
// operator writes NetworkPolicies to.
//
// Writing a NetworkPolicy and having one enforced are different events, and
// the write succeeds either way. Where the API server the operator holds is not
// the one the CNI watches -- a vcluster with networkPolicies sync off is the
// case that produced #172, but any split control plane has the same shape --
// the compiled backstop is an object with no reader. The apiserver stores it,
// the reconcile reports success, and the pod it claims to govern reaches the
// cloud metadata endpoint.
//
// The operator cannot see the host's data plane from inside such a cluster, so
// it cannot prove enforcement. What it can do is look for whoever would have to
// be here for enforcement to happen, and say so when nobody is.
type EnforcementVerdict string

const (
	// EnforcementObserved means something that enforces NetworkPolicy is
	// present in this API server. It is evidence of an enforcer, not proof of
	// enforcement: an agent can be installed and still be broken.
	EnforcementObserved EnforcementVerdict = "Observed"
	// EnforcementNotObserved means the probe ran, found nothing that enforces
	// NetworkPolicy, and therefore the written backstop is likely inert.
	EnforcementNotObserved EnforcementVerdict = "NotObserved"
	// EnforcementUnknown means the probe could not answer -- typically because
	// the operator may not list DaemonSets. Reported as doubt, never as a
	// failure: degrading on a question nobody could ask would page people over
	// working policies.
	EnforcementUnknown EnforcementVerdict = "Unknown"
)

// EnforcementSignal is a verdict plus the evidence behind it, so the status
// condition can name what was found instead of asserting a conclusion.
type EnforcementSignal struct {
	Verdict EnforcementVerdict
	// Source names the enforcer that was found, or why none could be.
	Source string
}

// defaultEnforcementProbeTTL is how long a verdict is reused. An enforcer is
// installed once per cluster lifetime, so re-asking on every reconcile of every
// policy buys nothing; the TTL keeps a newly installed CNI visible within a few
// minutes without a permanent watch.
const defaultEnforcementProbeTTL = 5 * time.Minute

// enforcerAPIs are API kinds whose presence means some controller in THIS API
// server compiles or enforces network policy. They are matched without a
// version: a cluster on antrea v1beta2 enforces just as one on v1beta1 does,
// and pinning the version would turn an upgrade into a false alarm.
var enforcerAPIs = []struct {
	GroupKind schema.GroupKind
	Name      string
}{
	{schema.GroupKind{Group: CiliumGroup, Kind: CiliumNetworkPolicyKind}, "Cilium"},
	{schema.GroupKind{Group: "crd.projectcalico.org", Kind: "GlobalNetworkPolicy"}, "Calico"},
	{schema.GroupKind{Group: "crd.antrea.io", Kind: "AntreaAgentInfo"}, "Antrea"},
	{schema.GroupKind{Group: "networking.k8s.aws", Kind: "PolicyEndpoint"}, "AWS VPC CNI"},
	{schema.GroupKind{Group: "kubeovn.io", Kind: "Subnet"}, "Kube-OVN"},
	{schema.GroupKind{Group: "k8s.ovn.org", Kind: "EgressFirewall"}, "OVN-Kubernetes"},
}

// enforcerDaemonSets are agent names that enforce NetworkPolicy without
// bringing a CRD of their own to recognize them by. Without this second look,
// a working Azure NPM or kube-router cluster would be told its backstop is
// inert -- the false alarm that makes an honest signal worthless.
var enforcerDaemonSets = map[string]string{
	"azure-npm":    "Azure NPM",
	"calico-node":  "Calico",
	"canal":        "Canal",
	"cilium":       "Cilium",
	"kube-ovn-cni": "Kube-OVN",
	"kube-router":  "kube-router",
	"ovnkube-node": "OVN-Kubernetes",
	"weave-net":    "Weave Net",
}

// EnforcementProbe answers whether anything in the API server the operator
// writes to is in a position to enforce the NetworkPolicies it writes there.
//
// The two questions it asks are deliberately cheap and read-only: which policy
// APIs this server serves (a RESTMapper lookup, no request) and which agents
// run in it (one DaemonSet list per TTL). Neither can see the data plane, which
// is why the verdict is evidence rather than proof.
type EnforcementProbe struct {
	// Mapper resolves API kinds served by this API server.
	Mapper meta.RESTMapper
	// Reader lists DaemonSets straight from the API server. It is deliberately
	// the uncached reader: caching would open a cluster-wide DaemonSet watch to
	// answer a question asked twelve times an hour.
	Reader client.Reader
	// Override, when set, short-circuits the probe. It is the escape hatch for
	// a cluster whose enforcer this code does not recognize (and for the
	// reverse: an operator knowingly writing into a split control plane).
	Override EnforcementVerdict
	// TTL bounds how long a verdict is reused; defaultEnforcementProbeTTL when zero.
	TTL time.Duration
	// Now is the clock, injectable for tests.
	Now func() time.Time

	mu      sync.Mutex
	cached  EnforcementSignal
	expires time.Time
}

// Signal returns the current verdict, probing at most once per TTL. A nil probe
// reports Unknown, so a reconciler wired without one degrades to doubt rather
// than to a claim.
func (p *EnforcementProbe) Signal(ctx context.Context) EnforcementSignal {
	if p == nil {
		return EnforcementSignal{Verdict: EnforcementUnknown, Source: "no enforcement probe is configured"}
	}
	if p.Override != "" {
		return EnforcementSignal{
			Verdict: p.Override,
			Source:  "asserted by the operator's --networkpolicy-enforcement flag, not by a look at this cluster",
		}
	}

	now := time.Now
	if p.Now != nil {
		now = p.Now
	}
	ttl := p.TTL
	if ttl <= 0 {
		ttl = defaultEnforcementProbeTTL
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	at := now()
	if at.Before(p.expires) {
		return p.cached
	}
	p.cached = p.probe(ctx)
	p.expires = at.Add(ttl)
	return p.cached
}

// probe looks for an enforcer, API surface first (free) and agents second.
func (p *EnforcementProbe) probe(ctx context.Context) EnforcementSignal {
	if p.Mapper != nil {
		for _, api := range enforcerAPIs {
			if _, err := p.Mapper.RESTMapping(api.GroupKind); err == nil {
				return EnforcementSignal{
					Verdict: EnforcementObserved,
					Source: fmt.Sprintf("%s: this API server serves %s",
						api.Name, strings.ToLower(api.GroupKind.Kind)+"."+api.GroupKind.Group),
				}
			}
		}
	}

	if p.Reader == nil {
		return EnforcementSignal{
			Verdict: EnforcementUnknown,
			Source:  "no policy-enforcing API is served here and agents could not be checked",
		}
	}

	var list appsv1.DaemonSetList
	if err := p.Reader.List(ctx, &list); err != nil {
		return EnforcementSignal{
			Verdict: EnforcementUnknown,
			Source:  fmt.Sprintf("no policy-enforcing API is served here and DaemonSets could not be listed: %v", err),
		}
	}
	var found []string
	for i := range list.Items {
		if name, ok := enforcerDaemonSets[list.Items[i].Name]; ok {
			found = append(found, fmt.Sprintf("%s (%s/%s)", name, list.Items[i].Namespace, list.Items[i].Name))
		}
	}
	if len(found) > 0 {
		sort.Strings(found)
		return EnforcementSignal{
			Verdict: EnforcementObserved,
			Source:  "agent running in this cluster: " + strings.Join(found, ", "),
		}
	}
	return EnforcementSignal{
		Verdict: EnforcementNotObserved,
		Source: "this API server serves no policy-enforcing API and runs no recognized CNI agent, " +
			"so a NetworkPolicy written here has no reader",
	}
}
