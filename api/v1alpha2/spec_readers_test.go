package v1alpha2_test

import (
	"bufio"
	"bytes"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
)

// readerPackages are the packages whose non-test code counts as a reader of a
// spec field: the reconcilers, the webhooks and what they call.
var readerPackages = []string{
	"github.com/mcp-hangar/operator/api/...", // helpers such as IsPaused
	"github.com/mcp-hangar/operator/cmd/...",
	"github.com/mcp-hangar/operator/internal/...",
	"github.com/mcp-hangar/operator/pkg/...",
}

// unreadAllowed lists spec fields that are in the schema although no Go code
// in readerPackages selects them. Every entry needs a reason a reviewer can
// check: the field is read by core, or only by a CEL rule on the CRD, or the
// operator reads it through a whole-struct copy the selector scan cannot see.
// "Nothing reads it yet" is not a reason: remove the field instead (#328).
var unreadAllowed = map[string]string{
	"UpstreamRule.Name": "names the rule for the person reading the manifest and claims no " +
		"behaviour; required, so removing it would reject every MCPEgressPolicy",
}

// TestEverySpecFieldHasAReader fails when a v1alpha2 spec field has no
// non-test reader. A declared field nothing acts on reads as behaviour in a
// manifest and in kubectl get -o yaml; 0.15.3 and #328 removed such fields,
// and this keeps new ones from landing.
//
// A read is any selector expression on the field outside _test.go and
// zz_generated files, so a field only copied somewhere else (a status mirror,
// a log line, a record) passes. That is how enforcementMode looked read
// while no branch acted on it: the guard catches a field nothing touches,
// not one whose value goes nowhere useful.
func TestEverySpecFieldHasAReader(t *testing.T) {
	if testing.Short() {
		t.Skip("type-checks the operator's packages")
	}
	read := selectedFields(t)

	var unread []string
	for _, f := range specFields() {
		if read[f.key] {
			if _, ok := unreadAllowed[f.key]; ok {
				t.Errorf("%s (%s) is in unreadAllowed but has a reader now: drop the entry", f.key, f.path)
			}
			continue
		}
		if _, ok := unreadAllowed[f.key]; ok {
			continue
		}
		unread = append(unread, f.key+" ("+f.path+")")
	}
	for key := range unreadAllowed {
		if !hasSpecField(key) {
			t.Errorf("unreadAllowed names %s, which is not a spec field", key)
		}
	}
	if len(unread) > 0 {
		sort.Strings(unread)
		t.Errorf("spec fields with no non-test reader in %v:\n  %s\n"+
			"Wire each into a reconciler or webhook, remove it from the API, or, if core "+
			"or a CEL rule reads it, add it to unreadAllowed with the reason.",
			readerPackages, strings.Join(unread, "\n  "))
	}
}

type specField struct {
	key  string // Go type and field, e.g. MCPServerSpec.Replicas
	path string // JSON path, e.g. MCPServer.spec.replicas
}

var apiPkgPath = reflect.TypeFor[mcpv1alpha2.MCPServer]().PkgPath()

// specFields walks the spec type of every kind, through pointers, slices and
// maps, into every struct this package declares. Types from other packages
// (corev1, metav1) are leaves: their fields are not ours to account for.
func specFields() []specField {
	roots := map[string]reflect.Type{
		"MCPServer":          reflect.TypeFor[mcpv1alpha2.MCPServerSpec](),
		"MCPServerGroup":     reflect.TypeFor[mcpv1alpha2.MCPServerGroupSpec](),
		"MCPDiscoverySource": reflect.TypeFor[mcpv1alpha2.MCPDiscoverySourceSpec](),
		"MCPEgressPolicy":    reflect.TypeFor[mcpv1alpha2.MCPEgressPolicySpec](),
	}
	var out []specField
	seen := map[reflect.Type]bool{}
	var walk func(t reflect.Type, path string)
	walk = func(t reflect.Type, path string) {
		for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Map {
			t = t.Elem()
		}
		if t.Kind() != reflect.Struct || t.PkgPath() != apiPkgPath || seen[t] {
			return
		}
		seen[t] = true
		for i := range t.NumField() {
			f := t.Field(i)
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "" {
				name = f.Name
			}
			out = append(out, specField{key: t.Name() + "." + f.Name, path: path + "." + name})
			walk(f.Type, path+"."+name)
		}
	}
	// MCPServer first, so its fields are named by their own path rather than
	// through the discovery source's providerTemplate, which embeds them.
	for _, kind := range []string{"MCPServer", "MCPServerGroup", "MCPDiscoverySource", "MCPEgressPolicy"} {
		walk(roots[kind], kind+".spec")
	}
	return out
}

func hasSpecField(key string) bool {
	for _, f := range specFields() {
		if f.key == key {
			return true
		}
	}
	return false
}

// selectedFields type-checks the non-test files of readerPackages and returns
// every field of this package that some selector expression reads, as
// "Type.Field". Imports resolve from the export data go list builds, so the
// check sees exactly what the compiler sees.
func selectedFields(t *testing.T) map[string]bool {
	t.Helper()
	args := append([]string{"list", "-export", "-deps", "-f",
		"{{.ImportPath}}\t{{.Export}}\t{{.Dir}}\t{{join .GoFiles \" \"}}"}, readerPackages...)
	cmd := exec.Command("go", args...)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	exports := map[string]string{}
	type pkg struct{ path, dir, files string }
	var ours []pkg
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		cols := strings.Split(sc.Text(), "\t")
		if len(cols) != 4 {
			t.Fatalf("go list: unexpected line %q", sc.Text())
		}
		exports[cols[0]] = cols[1]
		if strings.HasPrefix(cols[0], "github.com/mcp-hangar/operator/") {
			ours = append(ours, pkg{cols[0], cols[2], cols[3]})
		}
	}
	fset := token.NewFileSet()
	imp := importer.ForCompiler(fset, "gc", func(path string) (io.ReadCloser, error) {
		return os.Open(exports[path])
	})
	read := map[string]bool{}
	for _, p := range ours {
		var files []*ast.File
		for name := range strings.FieldsSeq(p.files) {
			f, err := parser.ParseFile(fset, filepath.Join(p.dir, name), nil, parser.SkipObjectResolution)
			if err != nil {
				t.Fatalf("parse %s: %v", name, err)
			}
			files = append(files, f)
		}
		info := &types.Info{Selections: map[*ast.SelectorExpr]*types.Selection{}}
		if _, err := (&types.Config{Importer: imp}).Check(p.path, fset, files, info); err != nil {
			t.Fatalf("type-check %s: %v", p.path, err)
		}
		for expr, sel := range info.Selections {
			// DeepCopy copies every field and acts on none.
			if sel.Kind() != types.FieldVal ||
				strings.HasPrefix(filepath.Base(fset.Position(expr.Pos()).Filename), "zz_generated") {
				continue
			}
			// Walk the index path so a promoted field is attributed to the
			// struct that declares it.
			typ := sel.Recv()
			for _, idx := range sel.Index() {
				st, named := structOf(typ)
				if st == nil {
					break
				}
				field := st.Field(idx)
				if named != nil && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == apiPkgPath {
					read[named.Obj().Name()+"."+field.Name()] = true
				}
				typ = field.Type()
			}
		}
	}
	return read
}

func structOf(t types.Type) (*types.Struct, *types.Named) {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	named, _ := t.(*types.Named)
	st, _ := t.Underlying().(*types.Struct)
	return st, named
}
