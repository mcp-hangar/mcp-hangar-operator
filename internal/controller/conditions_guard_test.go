package controller

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
)

// TestNoCallSitePassesAnEmptyConditionReason is the guard that makes #174
// non-recurring. The runtime fallback in upsertCondition keeps an empty reason
// from invalidating a whole status write, but on its own it would swallow the
// mistake; this fails the build on the mistake itself, at the call site, with
// a file and line.
//
// It only rules on string literals -- a reason built from a variable cannot be
// judged statically, and that residue is exactly what the runtime fallback is
// for. Every reason in the tree today is a literal.
func TestNoCallSitePassesAnEmptyConditionReason(t *testing.T) {
	// Index of the `reason` parameter in each condition helper.
	reasonArg := map[string]int{
		"setServerCondition": 3, // (server, condType, status, reason, message)
		"setGroupCondition":  3, // (group,  condType, status, reason, message)
		"upsertCondition":    4, // (conds, generation, condType, status, reason, message)
	}

	entries, err := os.ReadDir(".")
	require.NoError(t, err)

	fset := token.NewFileSet()
	callSites := 0

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		file, err := parser.ParseFile(fset, name, nil, 0)
		require.NoError(t, err)

		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			fn, ok := call.Fun.(*ast.Ident)
			if !ok {
				return true
			}
			idx, ok := reasonArg[fn.Name]
			if !ok || idx >= len(call.Args) {
				return true
			}
			callSites++

			lit, ok := call.Args[idx].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true // computed at runtime; the fallback covers it
			}
			reason, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			if reason == "" {
				t.Errorf("%s: %s passes an empty condition reason.\n"+
					"metav1.Condition.Reason is MinLength=1, so the apiserver rejects the "+
					"whole status subresource write, not just this condition -- every other "+
					"field the reconcile computed is discarded with it (#174). "+
					"Pass a CamelCase reason.",
					fset.Position(lit.Pos()), fn.Name)
			}
			return true
		})
	}

	// A scan that matched nothing would pass in silence, which is the failure
	// mode this whole test exists to avoid.
	require.Positive(t, callSites,
		"found no condition call sites to check -- the scan is not looking where the code is")
}

// The fail-safe itself: an empty reason that reaches the helper is replaced,
// so one bad reason costs one wrong-looking condition rather than the entire
// status write.
func TestUpsertCondition_EmptyReasonIsReplacedWithASchemaValidOne(t *testing.T) {
	server := &mcpv1alpha2.MCPServer{}

	setServerCondition(server, ConditionDegraded, metav1.ConditionFalse, "", "")

	cond := getCondition(server.Status.Conditions, ConditionDegraded)
	require.NotNil(t, cond)
	assert.Equal(t, ReasonUnspecified, cond.Reason)
	assert.NotEmpty(t, cond.Reason,
		"an empty reason fails CRD validation and takes the whole status write with it")
}

// A real reason is passed through untouched -- the fallback must not rewrite
// anything it was not asked to.
func TestUpsertCondition_RealReasonIsPreserved(t *testing.T) {
	server := &mcpv1alpha2.MCPServer{}

	setServerCondition(server, ConditionReady, metav1.ConditionTrue, "ProviderReady", "Provider is ready")

	cond := getCondition(server.Status.Conditions, ConditionReady)
	require.NotNil(t, cond)
	assert.Equal(t, "ProviderReady", cond.Reason)
}
