//go:build e2e_coco

// Package coco runs a kbs-attested key-pinned MCPServer end to end on
// kata-qemu-coco-dev: acceptance test T26 of ADR-030 as ADR-031 runs it
// (mcp-hangar/mcp-hangar#1950).
//
// The test is the provisioner. It generates the key pair, keeps the SPKI as the
// CR's pin, uploads only the bundle of private key and certificate to the KBS,
// and binds its release to the hash of the CR's init-data with
// kbs/resource-policy.rego. The init-data carries an agent policy generated
// with kata's genpolicy from what `manager render` prints for the CR, so the
// guest runs only the container the policy describes. Then:
//
//   - status.initDataDigests is the hash of the document the test bound;
//   - the pod gets the key from the KBS, serves TLS with it, core's handshake
//     check passes and a tools/call through the gateway succeeds (T1);
//   - kubectl exec into the pod is refused by the agent policy;
//   - a pod with other init-data gets no key and never serves.
//
// This proves the wiring, not confidentiality: the guest attests with
// Trustee's sample attester, whose evidence is unsigned, and the policy admits
// it. An honest guest reports the hash of the init-data it was given, which is
// what the refusal shows; a dishonest one could claim any hash.
//
// Requires the stack booted by up.sh (make e2e-coco-up).
package coco

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
	"github.com/mcp-hangar/operator/pkg/provider"
)

//go:embed kbs/resource-policy.rego
var resourcePolicy string

//go:embed upstream/server.py
var upstreamScript string

const (
	stackNamespace = "hangar-e2e"
	serverName     = "attested"
	keyPath        = "default/hangar-e2e/tls-key"
	keyRef         = "kbs:///" + keyPath
	// kbsURL is the fixed ClusterIP of manifests/kbs.yaml. The guest reaches the
	// KBS by the address the init-data names.
	kbsURL       = "http://10.96.200.10:8080"
	runtimeClass = "kata-qemu-coco-dev"
	// pythonImage is public and pinned by digest: the guest pulls it itself.
	pythonImage = "python:3.13-slim@sha256:57b29a7c050c4e679e4bea572e14b3965cf4b5839de2da49d55e49ffc4024f39"
	kbsLocal    = "127.0.0.1:18080"
	readyWithin = 15 * time.Minute
	// operatorImage is the build of this checkout up.sh loaded into kind.
	operatorImage = "localhost/mcp-hangar-operator:e2e"
)

// initData is the CR's init-data: where the guest's attestation agent and CDH
// find the KBS, and the key_ref the CRD requires it to name. extra makes a
// different document, and so a different hash.
func initData(extra string) string {
	return `version = "0.1.0"
algorithm = "sha256"
[data]
"aa.toml" = '''
[token_configs]
[token_configs.kbs]
url = "` + kbsURL + `"
'''
"cdh.toml" = '''
[kbc]
name = "cc_kbc"
url = "` + kbsURL + `"
'''
"key_ref" = "` + keyRef + `"
` + extra
}

func TestKBSAttestedEndToEnd(t *testing.T) {
	cl := kubeClient(t)
	bundle, pin := keyBundle(t)
	cr := buildServer(initData(""), pin)
	document := initData(agentPolicy(t, cr))
	cr.Spec.InitData = document
	digest := sha256.Sum256([]byte(document))

	stop := portForwardKBS(t)
	defer stop()
	provision(t, "resource-policy", "application/json", mustJSON(t, map[string]string{
		"policy": base64.RawURLEncoding.EncodeToString([]byte(strings.Replace(resourcePolicy,
			"@INIT_DATA_SHA256_B64@", base64.StdEncoding.EncodeToString(digest[:]), 1))),
	}))
	provision(t, "resource/"+keyPath, "application/octet-stream", bundle)

	createServer(t, cl, cr)
	srv := waitReady(t, cl)

	t.Run("status.initDataDigests is the hash the key is bound to", func(t *testing.T) {
		if srv.Status.InitDataDigests == nil || srv.Status.InitDataDigests.SHA256 != hex.EncodeToString(digest[:]) {
			t.Fatalf("status.initDataDigests = %+v, the KBS binds sha256 %x", srv.Status.InitDataDigests, digest)
		}
	})

	t.Run("a call through the gateway reaches the pinned key", func(t *testing.T) {
		out := gatewayCall(t)
		if !strings.Contains(out, `"success": true`) && !strings.Contains(out, `"success":true`) {
			t.Fatalf("hangar_call echo did not succeed: %s", out)
		}
	})

	t.Run("the agent policy refuses kubectl exec", func(t *testing.T) {
		out, err := exec.Command("kubectl", "-n", stackNamespace, "exec", srv.Status.PodName, "--",
			"python", "-c", "print('inside')").CombinedOutput()
		if err == nil || strings.Contains(string(out), "inside") {
			t.Fatalf("kubectl exec ran in the guest: %s", out)
		}
		if !strings.Contains(string(out), "ExecProcessRequest") {
			t.Fatalf("kubectl exec failed, but not on the agent policy: %s", out)
		}
		t.Logf("kubectl exec refused: %s", strings.TrimSpace(string(out)))
	})

	t.Run("a pod with other init-data gets no key", func(t *testing.T) {
		logs, podIP := podWithOtherInitData(t, cl)
		if !strings.Contains(logs, "did not release") {
			t.Fatalf("the pod with other init-data did not report a refused key; logs:\n%s", logs)
		}
		// The refusal is the KBS's: the pod asked for the key and was answered
		// with something other than 200, every time.
		out, err := exec.Command("kubectl", "-n", "trustee", "logs", "deploy/kbs").CombinedOutput()
		if err != nil {
			t.Fatalf("read the KBS log: %v: %s", err, out)
		}
		var asked, released int
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(line, podIP+" ") && strings.Contains(line, "GET /kbs/v0/resource/"+keyPath+" ") {
				asked++
				if strings.Contains(line, " 200 ") {
					released++
				}
			}
		}
		if asked == 0 || released != 0 {
			t.Fatalf("the KBS log shows %d requests for the key from %s, %d of them released", asked, podIP, released)
		}
		t.Logf("the KBS refused all %d requests for the key from the pod with other init-data", asked)
	})
}

func kubeClient(t *testing.T) crclient.Client {
	t.Helper()
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		kubeconfig = os.Getenv("HOME") + "/.kube/config"
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		t.Fatalf("kubeconfig: %v", err)
	}
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add client-go scheme: %v", err)
	}
	if err := mcpv1alpha2.AddToScheme(scheme); err != nil {
		t.Fatalf("add mcp scheme: %v", err)
	}
	cl, err := crclient.New(cfg, crclient.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	return cl
}

// keyBundle generates the upstream's P-256 key and a self-signed certificate
// for it, and returns them as one PEM bundle with the pin: the SHA-256 of the
// SPKI as the certificate encodes it.
func keyBundle(t *testing.T) ([]byte, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "kbs-attested-upstream"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	var bundle bytes.Buffer
	_ = pem.Encode(&bundle, &pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})
	_ = pem.Encode(&bundle, &pem.Block{Type: "CERTIFICATE", Bytes: der})
	spki := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return bundle.Bytes(), "sha256:" + hex.EncodeToString(spki[:])
}

func portForwardKBS(t *testing.T) func() {
	t.Helper()
	cmd := exec.Command("kubectl", "-n", "trustee", "port-forward", "svc/kbs", "18080:8080")
	if err := cmd.Start(); err != nil {
		t.Fatalf("port-forward the KBS: %v", err)
	}
	for i := 0; i < 30; i++ {
		if conn, err := net.DialTimeout("tcp", kbsLocal, time.Second); err == nil {
			_ = conn.Close()
			return func() { _ = cmd.Process.Kill() }
		}
		time.Sleep(time.Second)
	}
	_ = cmd.Process.Kill()
	t.Fatal("the KBS port-forward never answered")
	return nil
}

// adminToken is a bearer JWT with the provisioner role, signed by the admin key
// up.sh generated and the KBS trusts.
func adminToken(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(stateDir() + "/kbs-admin.key")
	if err != nil {
		t.Fatalf("read the KBS admin key: %v", err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		t.Fatal("the KBS admin key is not PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatalf("parse the KBS admin key: %v", err)
	}
	key, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		t.Fatalf("the KBS admin key is %T, not Ed25519", parsed)
	}
	enc := base64.RawURLEncoding
	now := time.Now().Unix()
	signing := enc.EncodeToString(mustJSON(t, map[string]string{"alg": "EdDSA", "typ": "JWT"})) + "." +
		enc.EncodeToString(mustJSON(t, map[string]any{"role": "provisioner", "iat": now, "exp": now + 600}))
	return signing + "." + enc.EncodeToString(ed25519.Sign(key, []byte(signing)))
}

func provision(t *testing.T, path, contentType string, body []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "http://"+kbsLocal+"/kbs/v0/"+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer "+adminToken(t))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("provision %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		var msg bytes.Buffer
		_, _ = msg.ReadFrom(resp.Body)
		t.Fatalf("provision %s: %s: %s", path, resp.Status, msg.String())
	}
}

// buildServer is the kbs-attested MCPServer the test runs, with document as
// its init-data.
func buildServer(document, pin string) *mcpv1alpha2.MCPServer {
	rc, noToken, nonRoot, nobody := runtimeClass, false, true, int64(65534)
	return &mcpv1alpha2.MCPServer{
		TypeMeta:   metav1.TypeMeta{APIVersion: mcpv1alpha2.GroupVersion.String(), Kind: "MCPServer"},
		ObjectMeta: metav1.ObjectMeta{Name: serverName, Namespace: stackNamespace},
		Spec: mcpv1alpha2.MCPServerSpec{
			Mode:                         mcpv1alpha2.MCPServerModeContainer,
			Image:                        pythonImage,
			Command:                      []string{"python", "-c"},
			Args:                         []string{upstreamScript},
			Env:                          []corev1.EnvVar{{Name: "KBS_RESOURCE", Value: keyPath}},
			RuntimeClassName:             &rc,
			AutomountServiceAccountToken: &noToken,
			StartupTimeout:               &metav1.Duration{Duration: readyWithin},
			// The guest pulls the image, so genpolicy cannot take the user from
			// it and wants the group stated; the rest is the operator's default.
			PodSecurityContext: &corev1.PodSecurityContext{
				RunAsNonRoot:   &nonRoot,
				RunAsUser:      &nobody,
				RunAsGroup:     &nobody,
				FSGroup:        &nobody,
				SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
			},
			TLS: &mcpv1alpha2.MCPServerTLS{PeerIdentity: &mcpv1alpha2.PeerIdentity{
				KeyRef:     keyRef,
				SPKISHA256: []string{pin},
				Issuance:   mcpv1alpha2.PeerKeyIssuanceKBSAttested,
			}},
			InitData: document,
		},
	}
}

// agentPolicy generates the Kata agent policy for cr the way ADR-031 says a
// provisioner does: `manager render` from the operator image under test
// prints the Deployment, and genpolicy from the kata release up.sh fetched
// turns it into Rego. It returns the policy as an init-data entry. The CR is
// rendered as created, at generation 1, because the pod template carries it.
func agentPolicy(t *testing.T, cr *mcpv1alpha2.MCPServer) string {
	t.Helper()
	created := cr.DeepCopy()
	created.Generation = 1
	manifest, err := yaml.Marshal(created)
	if err != nil {
		t.Fatal(err)
	}
	render := exec.Command("docker", "run", "--rm", "-i", operatorImage, "render")
	render.Stdin = bytes.NewReader(manifest)
	deployment, err := render.Output()
	if err != nil {
		t.Fatalf("manager render: %v: %s", err, stderrOf(err))
	}
	// genpolicy (kata 4.2.0) refuses any field it does not model, and it does
	// not model four that the operator renders for a kbs-attested pod. Each is
	// checked to hold the value that makes dropping it safe, then dropped one by
	// one, rather than passing -s, which would silence every unknown field:
	//   - automountServiceAccountToken false: no token is mounted;
	//   - runAsNonRoot true on the pod and the container: the policy pins the
	//     UID and GID themselves, 65534 from the pod's securityContext;
	//   - the pod's seccompProfile RuntimeDefault: the container's own,
	//     which genpolicy reads, says the same.
	var doc map[string]any
	if err := yaml.Unmarshal(deployment, &doc); err != nil {
		t.Fatalf("read the rendered Deployment: %v", err)
	}
	podSpec := doc["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	podSecurity := podSpec["securityContext"].(map[string]any)
	containerSecurity := podSpec["containers"].([]any)[0].(map[string]any)["securityContext"].(map[string]any)
	for _, drop := range []struct {
		from  map[string]any
		field string
		safe  any
	}{
		{podSpec, "automountServiceAccountToken", false},
		{podSecurity, "runAsNonRoot", true},
		{containerSecurity, "runAsNonRoot", true},
		{podSecurity, "seccompProfile", map[string]any{"type": "RuntimeDefault"}},
	} {
		if fmt.Sprint(drop.from[drop.field]) != fmt.Sprint(drop.safe) {
			t.Fatalf("the rendered pod has %s %v, not %v, so it cannot be dropped for genpolicy", drop.field, drop.from[drop.field], drop.safe)
		}
		delete(drop.from, drop.field)
	}
	delete(doc, "status")
	if deployment, err = yaml.Marshal(doc); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/deployment.yaml", deployment, 0o600); err != nil {
		t.Fatal(err)
	}
	kata := stateDir() + "/kata-tools/opt/kata"
	gen := exec.Command(kata+"/bin/genpolicy", "-r", "-y", dir+"/deployment.yaml",
		"-p", kata+"/share/defaults/kata-containers/rules.rego",
		"-j", kata+"/share/defaults/kata-containers/genpolicy-settings.json")
	gen.Dir = dir
	policy, err := gen.Output()
	if err != nil {
		t.Fatalf("genpolicy: %v: %s", err, stderrOf(err))
	}
	const literal = "'''"
	if strings.Contains(string(policy), literal) {
		t.Fatal("the generated policy contains ''' and cannot sit in a TOML literal string")
	}
	return `"policy.rego" = ` + literal + "\n" + strings.TrimRight(string(policy), "\n") + "\n" + literal + "\n"
}

func stderrOf(err error) []byte {
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.Stderr
	}
	return nil
}

func createServer(t *testing.T, cl crclient.Client, cr *mcpv1alpha2.MCPServer) {
	t.Helper()
	ctx := context.Background()
	old := &mcpv1alpha2.MCPServer{ObjectMeta: metav1.ObjectMeta{Name: serverName, Namespace: stackNamespace}}
	if err := cl.Delete(ctx, old); err != nil && !apierrors.IsNotFound(err) {
		t.Fatalf("delete leftover: %v", err)
	}
	if err := cl.Create(ctx, cr); err != nil {
		t.Fatalf("create the kbs-attested MCPServer: %v", err)
	}
}

func waitReady(t *testing.T, cl crclient.Client) *mcpv1alpha2.MCPServer {
	t.Helper()
	deadline := time.Now().Add(readyWithin)
	srv := &mcpv1alpha2.MCPServer{}
	for time.Now().Before(deadline) {
		if err := cl.Get(context.Background(), types.NamespacedName{Name: serverName, Namespace: stackNamespace}, srv); err != nil {
			t.Fatalf("get MCPServer: %v", err)
		}
		if apimeta.IsStatusConditionTrue(srv.Status.Conditions, "Ready") {
			return srv
		}
		if c := apimeta.FindStatusCondition(srv.Status.Conditions, "Ready"); c != nil {
			t.Logf("Ready=%s %s: %s", c.Status, c.Reason, c.Message)
		}
		time.Sleep(15 * time.Second)
	}
	t.Fatalf("the kbs-attested MCPServer was not Ready within %s: %+v", readyWithin, srv.Status)
	return nil
}

// gatewayCall calls echo on the server through the gateway's hangar_call, from
// inside the core pod, with the MCP SDK the image ships.
func gatewayCall(t *testing.T) string {
	t.Helper()
	script := `import asyncio, json, sys
from mcp.client.session import ClientSession
from mcp.client.streamable_http import streamable_http_client

async def main():
    async with streamable_http_client("http://localhost:8080/mcp") as (read, write, *_):
        async with ClientSession(read, write) as session:
            await session.initialize()
            res = await session.call_tool("hangar_call", {"calls": [{"mcp_server": sys.argv[1], "tool": "echo", "arguments": {"text": "hi"}}]})
            print(json.dumps(json.loads(res.content[0].text)["results"][0]))

asyncio.run(main())
`
	out, err := exec.Command("kubectl", "-n", stackNamespace, "exec", "deploy/mcp-hangar", "-c", "core", "--",
		"python3", "-c", script, serverName).CombinedOutput()
	if err != nil {
		t.Fatalf("hangar_call echo in core: %v: %s", err, out)
	}
	t.Logf("hangar_call echo: %s", strings.TrimSpace(string(out)))
	return string(out)
}

// podWithOtherInitData runs the upstream as a bare pod on the same runtime with
// init-data that differs by one comment, and returns its logs and address once
// it has exited.
func podWithOtherInitData(t *testing.T, cl crclient.Client) (string, string) {
	t.Helper()
	ctx := context.Background()
	runAsNonRoot, uid, noToken := true, int64(65534), false
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "other-init-data", Namespace: stackNamespace,
			Annotations: map[string]string{provider.AnnotationKataInitData: provider.EncodeInitData(initData("# another workload\n"))},
		},
		Spec: corev1.PodSpec{
			RuntimeClassName:             ptr(runtimeClass),
			RestartPolicy:                corev1.RestartPolicyNever,
			AutomountServiceAccountToken: &noToken,
			SecurityContext:              &corev1.PodSecurityContext{RunAsNonRoot: &runAsNonRoot, RunAsUser: &uid},
			Containers: []corev1.Container{{
				Name: "upstream", Image: pythonImage, Command: []string{"python", "-c"}, Args: []string{upstreamScript},
				Env: []corev1.EnvVar{{Name: "KBS_RESOURCE", Value: keyPath}, {Name: "KEY_WAIT_S", Value: "90"}},
			}},
		},
	}
	_ = cl.Delete(ctx, pod)
	time.Sleep(5 * time.Second)
	if err := cl.Create(ctx, pod); err != nil {
		t.Fatalf("create the pod with other init-data: %v", err)
	}
	deadline := time.Now().Add(readyWithin)
	for time.Now().Before(deadline) {
		if err := cl.Get(ctx, crclient.ObjectKeyFromObject(pod), pod); err != nil {
			t.Fatalf("get the pod with other init-data: %v", err)
		}
		switch pod.Status.Phase {
		case corev1.PodSucceeded:
			t.Fatal("the pod with other init-data exited 0: it started without a released key")
		case corev1.PodFailed:
			out, _ := exec.Command("kubectl", "-n", stackNamespace, "logs", pod.Name).CombinedOutput()
			return string(out), pod.Status.PodIP
		}
		time.Sleep(10 * time.Second)
	}
	out, _ := exec.Command("kubectl", "-n", stackNamespace, "logs", pod.Name).CombinedOutput()
	t.Fatalf("the pod with other init-data did not exit within %s (phase %s); logs:\n%s", readyWithin, pod.Status.Phase, out)
	return "", ""
}

// stateDir is where up.sh left the KBS admin key and the kata tools.
func stateDir() string {
	if state := os.Getenv("E2E_COCO_STATE"); state != "" {
		return state
	}
	return "/tmp/e2e-coco"
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(fmt.Errorf("marshal: %w", err))
	}
	return b
}

func ptr[T any](v T) *T { return &v }
