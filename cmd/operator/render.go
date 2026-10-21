package main

import (
	"errors"
	"fmt"
	"io"

	appsv1 "k8s.io/api/apps/v1"
	"sigs.k8s.io/yaml"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
	"github.com/mcp-hangar/operator/pkg/provider"
)

// renderCommand is the dry-run of ADR-031 §4: `manager render < mcpserver.yaml`
// prints the Deployment this operator would apply for a container-mode
// MCPServer. For a kbs-attested server that is the input Kata's genpolicy
// takes, so the agent policy can be generated against exactly what will run.
// It runs in the operator's own image, so its rendering is that release's.
const renderCommand = "render"

// render reads one MCPServer manifest from in and writes the Deployment its
// pod would run under. The owner reference and the deployment-hash annotation
// the controller adds name the live object only; the pod template is complete.
// The template carries the manifest's metadata.generation, so to match what
// runs, feed it the stored object (`kubectl get mcpserver NAME -o yaml`).
func render(in io.Reader, out io.Writer) error {
	raw, err := io.ReadAll(in)
	if err != nil {
		return err
	}
	server := &mcpv1alpha2.MCPServer{}
	if err := yaml.UnmarshalStrict(raw, server); err != nil {
		return fmt.Errorf("reading the MCPServer: %w", err)
	}
	if server.Spec.Mode != mcpv1alpha2.MCPServerModeContainer {
		return errors.New("only a container-mode MCPServer renders a Deployment")
	}
	deployment, err := provider.BuildDeploymentForMCPServer(server)
	if err != nil {
		return err
	}
	deployment.TypeMeta.APIVersion = appsv1.SchemeGroupVersion.String()
	deployment.TypeMeta.Kind = "Deployment"
	encoded, err := yaml.Marshal(deployment)
	if err != nil {
		return err
	}
	_, err = out.Write(encoded)
	return err
}
