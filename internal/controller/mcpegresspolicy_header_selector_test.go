package controller

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
)

// An MCPEgressPolicy can express the Mcp-Param-* selector core enforces (#160).
//
// Before this, the selector existed in core (mcp-hangar/mcp-hangar#1058) and
// was reachable only over REST: the CRD had no field that compiled into it, so
// the declarative path could not say something the gateway already enforced.

func headerMatch(name string, values ...string) mcpv1alpha2.HeaderMatch {
	return mcpv1alpha2.HeaderMatch{Name: name, Values: values}
}

func TestCompileL7Policy_CompilesHeaderSelectors(t *testing.T) {
	p := testPolicy("pol", "default")
	p.Spec.Upstreams = []mcpv1alpha2.UpstreamRule{
		{
			Name:  "a",
			Match: mcpv1alpha2.UpstreamMatch{Host: "a.com"},
			Headers: &mcpv1alpha2.HeaderRules{
				Deny:            []mcpv1alpha2.HeaderMatch{headerMatch("Mcp-Param-Region", "us-*")},
				RequireApproval: []mcpv1alpha2.HeaderMatch{headerMatch("Mcp-Param-Tier", "free")},
			},
		},
		{
			Name:  "b",
			Match: mcpv1alpha2.UpstreamMatch{Host: "b.com"},
			Headers: &mcpv1alpha2.HeaderRules{
				Deny:  []mcpv1alpha2.HeaderMatch{headerMatch("Mcp-Param-Region", "us-*")}, // same rule, both upstreams
				Allow: []mcpv1alpha2.HeaderMatch{headerMatch("Mcp-Param-Region", "eu-*")},
			},
		},
	}

	out := compileL7Policy(p)

	require.NotNil(t, out.Headers)
	assert.Len(t, out.Headers.Deny, 1, "an identical selector on two upstreams is one rule")
	assert.Equal(t, "Mcp-Param-Region", out.Headers.Deny[0].Name)
	assert.Equal(t, []string{"us-*"}, out.Headers.Deny[0].Values)
	assert.Len(t, out.Headers.Allow, 1)
	assert.Len(t, out.Headers.RequireApproval, 1)
}

func TestCompileL7Policy_SameHeaderDifferentValuesAreBothKept(t *testing.T) {
	// Two rules on one header are alternatives, not a conflict -- core scans
	// them in order and takes the first that matches.
	p := testPolicy("pol", "default")
	p.Spec.Upstreams = []mcpv1alpha2.UpstreamRule{{
		Name:  "a",
		Match: mcpv1alpha2.UpstreamMatch{Host: "a.com"},
		Headers: &mcpv1alpha2.HeaderRules{Deny: []mcpv1alpha2.HeaderMatch{
			headerMatch("Mcp-Param-Region", "us-*"),
			headerMatch("Mcp-Param-Region", "ap-*"),
		}},
	}}

	out := compileL7Policy(p)

	require.NotNil(t, out.Headers)
	assert.Len(t, out.Headers.Deny, 2)
}

func TestCompileL7Policy_NoSelectorsOmitsTheBlockEntirely(t *testing.T) {
	// A policy that declares none must serialize exactly as it did before the
	// field existed, so an older core sees nothing new.
	p := testPolicy("pol", "default")
	p.Spec.Upstreams = []mcpv1alpha2.UpstreamRule{{
		Name:  "a",
		Match: mcpv1alpha2.UpstreamMatch{Host: "a.com"},
		Tools: &mcpv1alpha2.ToolRules{Allow: []string{"*"}},
	}}

	body, err := json.Marshal(compileL7Policy(p))

	require.NoError(t, err)
	assert.NotContains(t, string(body), "headers")
}

func TestCompileL7Policy_HeaderValuesAreCopiedNotAliased(t *testing.T) {
	// The compiled document outlives the CR object it was built from.
	values := []string{"us-*"}
	p := testPolicy("pol", "default")
	p.Spec.Upstreams = []mcpv1alpha2.UpstreamRule{{
		Name:    "a",
		Match:   mcpv1alpha2.UpstreamMatch{Host: "a.com"},
		Headers: &mcpv1alpha2.HeaderRules{Deny: []mcpv1alpha2.HeaderMatch{{Name: "Mcp-Param-Region", Values: values}}},
	}}

	out := compileL7Policy(p)
	values[0] = "mutated"

	assert.Equal(t, []string{"us-*"}, out.Headers.Deny[0].Values)
}

func TestCompileL7Policy_TheWireShapeIsWhatCoreParses(t *testing.T) {
	// Core's L7Policy.from_dict expects headers.<bucket>[].{name,values}.
	p := testPolicy("pol", "default")
	p.Spec.Upstreams = []mcpv1alpha2.UpstreamRule{{
		Name:    "a",
		Match:   mcpv1alpha2.UpstreamMatch{Host: "a.com"},
		Headers: &mcpv1alpha2.HeaderRules{Deny: []mcpv1alpha2.HeaderMatch{headerMatch("Mcp-Param-Region", "us-*")}},
	}}

	body, err := json.Marshal(compileL7Policy(p))
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(body, &decoded))
	headers, ok := decoded["headers"].(map[string]any)
	require.True(t, ok, "headers must be an object")
	deny, ok := headers["deny"].([]any)
	require.True(t, ok, "headers.deny must be a list")
	entry := deny[0].(map[string]any)
	assert.Equal(t, "Mcp-Param-Region", entry["name"])
	assert.Equal(t, []any{"us-*"}, entry["values"])
}
