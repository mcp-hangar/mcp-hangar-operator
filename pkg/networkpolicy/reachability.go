package networkpolicy

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

// Does a NetworkPolicy written here block a packet (#393)? Two canaries of the
// operator's image may reach each other on CanaryControlPort only; one is
// asked to dial the other on that port and on CanaryDeniedPort. Only a control
// that goes through next to a denied dial that does not is a block.
const (
	// CanaryCommand is the manager subcommand the canaries run.
	CanaryCommand = "enforcement-canary"
	// LabelEnforcementProbe marks the canaries and their policy.
	LabelEnforcementProbe = "mcp-hangar.io/enforcement-probe"
	// CanaryControlPort is where the operator reaches a canary and the port
	// the fence allows: the operator chart's own NetworkPolicy always lets the
	// operator out on 6443, so another port would read Unverified everywhere.
	CanaryControlPort = 6443
	// CanaryDeniedPort is the second listener, which the fence does not allow.
	CanaryDeniedPort = 6444

	canaryName     = "mcp-hangar-enforcement-probe"
	canaryDialWait = 3 * time.Second
	policySettle   = 30 * time.Second
	retryUnknown   = 15 * time.Second
	probeInterval  = 5 * time.Minute
)

// ReachabilityProbe is a leader-elected Runnable that keeps the last verdict;
// Result never blocks a reconcile. The canaries run the image of the
// operator's own pod (Namespace/PodName) and are owned by it. Updates, when
// set, gets an event on each verdict change so dependent status is re-read.
type ReachabilityProbe struct {
	Client             client.Client
	Reader             client.Reader // uncached
	Namespace, PodName string
	Updates            chan event.GenericEvent
	// Dial is the connection hook, net.Dialer by default.
	Dial func(ctx context.Context, addr string) (net.Conn, error)

	mu      sync.Mutex
	verdict EnforcementVerdict
	source  string
}

// Result returns the last verdict and the evidence behind it.
func (p *ReachabilityProbe) Result() (EnforcementVerdict, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.verdict == "" {
		return EnforcementUnknown, "the operator's reachability probe has not completed a round yet"
	}
	return p.verdict, p.source
}

// NeedLeaderElection keeps the canaries to one operator replica.
func (p *ReachabilityProbe) NeedLeaderElection() bool { return true }

// Start probes at once, again soon while in doubt, else every probeInterval.
func (p *ReachabilityProbe) Start(ctx context.Context) error {
	for {
		verdict, source := p.Round(ctx)
		p.record(verdict, source)
		wait := probeInterval
		if verdict == EnforcementUnknown {
			wait = retryUnknown
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
	}
}

func (p *ReachabilityProbe) record(verdict EnforcementVerdict, source string) {
	p.mu.Lock()
	changed := verdict != p.verdict
	p.verdict, p.source = verdict, source
	p.mu.Unlock()
	if changed && p.Updates != nil {
		select {
		case p.Updates <- event.GenericEvent{Object: &corev1.Pod{}}:
		default: // a pending event already covers this change
		}
	}
}

// Round makes sure the canaries exist and asks one to dial the other.
func (p *ReachabilityProbe) Round(ctx context.Context) (EnforcementVerdict, string) {
	unknown := func(format string, args ...any) (EnforcementVerdict, string) {
		return EnforcementUnknown, "the reachability probe " + fmt.Sprintf(format, args...)
	}
	if p.Namespace == "" || p.PodName == "" {
		return unknown("does not know the operator's own pod")
	}
	var self corev1.Pod
	if err := p.Reader.Get(ctx, client.ObjectKey{Namespace: p.Namespace, Name: p.PodName}, &self); err != nil {
		return unknown("could not read the operator's pod: %v", err)
	}
	fence := CanaryPolicy(&self)
	for _, obj := range []client.Object{CanaryDeployment(&self), fence} {
		if err := p.Client.Create(ctx, obj); err != nil && !apierrors.IsAlreadyExists(err) {
			return unknown("could not create %s: %v", obj.GetName(), err)
		}
	}
	if err := p.Reader.Get(ctx, client.ObjectKeyFromObject(fence), fence); err != nil {
		return unknown("could not read its policy: %v", err)
	}
	var pods corev1.PodList
	if err := p.Reader.List(ctx, &pods, client.InNamespace(p.Namespace),
		client.MatchingLabels{LabelEnforcementProbe: "canary"}); err != nil {
		return unknown("could not list its canaries: %v", err)
	}
	var ips []string
	settled := time.Since(fence.CreationTimestamp.Time) >= policySettle
	for i := range pods.Items {
		st := pods.Items[i].Status
		if st.Phase == corev1.PodRunning && st.PodIP != "" && pods.Items[i].DeletionTimestamp == nil {
			ips = append(ips, st.PodIP)
			// The CNI needs a moment to program a new pod as well as a new policy.
			settled = settled && st.StartTime != nil && time.Since(st.StartTime.Time) >= policySettle
		}
	}
	if len(ips) < 2 || !settled {
		return unknown("is waiting for its two canaries to run behind a programmed NetworkPolicy")
	}
	control, denied, err := p.ask(ctx, ips[0], ips[1])
	if err != nil {
		return unknown("could not reach its canary: %v", err)
	}
	return Verdict(control, denied)
}

// Verdict maps the canary's two dials onto an enforcement verdict.
func Verdict(control, denied bool) (EnforcementVerdict, string) {
	switch {
	case !control:
		return EnforcementUnknown, fmt.Sprintf("the reachability probe's control connection (port %d) "+
			"failed, so a blocked one would prove nothing", CanaryControlPort)
	case denied:
		return EnforcementNotObserved, fmt.Sprintf("the operator's reachability probe connected on port %d, "+
			"which its NetworkPolicy denies: a NetworkPolicy written here is not enforced", CanaryDeniedPort)
	default:
		return EnforcementObserved, fmt.Sprintf("the operator's reachability probe observed its NetworkPolicy "+
			"block port %d while the allowed port %d went through", CanaryDeniedPort, CanaryControlPort)
	}
}

// ask has the canary at fromIP dial toIP on both ports and reports what it reached.
func (p *ReachabilityProbe) ask(ctx context.Context, fromIP, toIP string) (control, denied bool, err error) {
	dial := p.Dial
	if dial == nil {
		dial = func(ctx context.Context, addr string) (net.Conn, error) {
			return (&net.Dialer{Timeout: canaryDialWait}).DialContext(ctx, "tcp", addr)
		}
	}
	conn, err := dial(ctx, net.JoinHostPort(fromIP, strconv.Itoa(CanaryControlPort)))
	if err != nil {
		return false, false, err
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(4 * canaryDialWait))
	if _, err := fmt.Fprintf(conn, "dial %s\n", toIP); err != nil {
		return false, false, err
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return false, false, err
	}
	var c, d string
	if _, err := fmt.Sscanf(line, "control=%s denied=%s", &c, &d); err != nil {
		return false, false, fmt.Errorf("unexpected canary reply %q", strings.TrimSpace(line))
	}
	return c == "open", d == "open", nil
}

// CanaryPolicy lets the canaries reach each other on the control port and
// nothing else. Every canary is fenced, so one that dials on request reaches
// only the other canary's ports.
func CanaryPolicy(self *corev1.Pod) *networkingv1.NetworkPolicy {
	tcp, port := corev1.ProtocolTCP, intstr.FromInt32(CanaryControlPort)
	canaries := metav1.LabelSelector{MatchLabels: map[string]string{LabelEnforcementProbe: "canary"}}
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: canaryName, Namespace: self.Namespace, OwnerReferences: ownedBy(self),
			Labels: map[string]string{LabelEnforcementProbe: "policy"}},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: canaries,
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
			Egress: []networkingv1.NetworkPolicyEgressRule{{
				To:    []networkingv1.NetworkPolicyPeer{{PodSelector: &canaries}},
				Ports: []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: &port}},
			}},
		},
	}
}

// CanaryDeployment runs the two canaries in the operator's own image, owned by
// the operator's pod so a rollout or an uninstall takes them along.
func CanaryDeployment(self *corev1.Pod) *appsv1.Deployment {
	labels := map[string]string{LabelEnforcementProbe: "canary"}
	image := ""
	if len(self.Spec.Containers) > 0 {
		image = self.Spec.Containers[0].Image
	}
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: canaryName, Namespace: self.Namespace, Labels: labels,
			OwnerReferences: ownedBy(self)},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To[int32](2),
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					AutomountServiceAccountToken: ptr.To(false),
					ImagePullSecrets:             self.Spec.ImagePullSecrets,
					SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: ptr.To(true),
						SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
					Containers: []corev1.Container{{
						Name: "canary", Image: image, Command: []string{"/manager"}, Args: []string{CanaryCommand},
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("5m"),
								corev1.ResourceMemory: resource.MustParse("16Mi")},
						},
						SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: ptr.To(false),
							ReadOnlyRootFilesystem: ptr.To(true),
							Capabilities:           &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
					}},
				},
			},
		},
	}
}

func ownedBy(self *corev1.Pod) []metav1.OwnerReference {
	return []metav1.OwnerReference{{APIVersion: "v1", Kind: "Pod", Name: self.Name, UID: self.UID}}
}

// RunCanary serves both canary ports until ctx ends. A "dial <ip>" line on the
// control port makes it dial ip on both ports and reply
// "control=open|closed denied=open|closed".
func RunCanary(ctx context.Context) error {
	for _, port := range []int{CanaryControlPort, CanaryDeniedPort} {
		ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", ":"+strconv.Itoa(port))
		if err != nil {
			return err
		}
		go func() { <-ctx.Done(); _ = ln.Close() }()
		go func() {
			for conn, err := ln.Accept(); err == nil; conn, err = ln.Accept() {
				go serveCanary(ctx, conn, port == CanaryControlPort)
			}
		}()
	}
	<-ctx.Done()
	return nil
}

func serveCanary(ctx context.Context, conn net.Conn, relay bool) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(4 * canaryDialWait))
	line, err := bufio.NewReader(conn).ReadString('\n')
	ip := net.ParseIP(strings.TrimSpace(strings.TrimPrefix(line, "dial ")))
	if !relay || err != nil || ip == nil {
		return
	}
	reach := func(port int) string {
		c, err := (&net.Dialer{Timeout: canaryDialWait}).DialContext(ctx, "tcp",
			net.JoinHostPort(ip.String(), strconv.Itoa(port)))
		if err != nil {
			return "closed"
		}
		_ = c.Close()
		return "open"
	}
	_, _ = fmt.Fprintf(conn, "control=%s denied=%s\n", reach(CanaryControlPort), reach(CanaryDeniedPort))
}
