// Every URL this client builds must exist in core's published route inventory.
//
// This is the test that was missing. The client called /api/v1/* for months
// after core moved to /api/*, and every existing test passed throughout: they
// assert against an httptest mock, and a mock answers whatever it is asked
// (#91). A wrong path is invisible to a fake that has no opinion about paths.
//
// The fix is not a better mock. It is checking against something authoritative:
// core generates pkg/hangar/testdata/api-routes.json from its own routing table
// and publishes it as a cross-repo contract (ADR-011, core#664). This vendors a
// copy and asserts the client agrees with it.
//
// The URLs are captured at runtime rather than parsed out of the source. What
// matters is the request that goes on the wire, not the string literal that
// produced it -- an fmt.Sprintf with the arguments in the wrong order builds a
// plausible-looking literal and a broken URL.
//
// # What this does not cover
//
// Paths only. If core renamed `consecutive_failures`, every assertion here
// would still pass and GetMCPServerHealth would decode zeros. Shape drift needs
// a smoke test against a running core; the inventory is honest about the same
// limit on its own side.
package hangar

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const inventoryPath = "testdata/api-routes.json"

// nonAPIRoutes are paths core serves outside the /api mount, which the
// inventory therefore does not describe. Kept short and justified: every entry
// here is knowledge duplicated by hand, which is the thing this file exists to
// reduce.
var nonAPIRoutes = map[string]string{
	"/health/live": "core's liveness probe, mounted in server/lifecycle.py alongside /health/ready and /health/startup",
}

type routeInventory struct {
	Mount  string `json:"mount"`
	Count  int    `json:"count"`
	Routes []struct {
		Path    string   `json:"path"`
		Methods []string `json:"methods"`
	} `json:"routes"`
}

func loadInventory(t *testing.T) routeInventory {
	t.Helper()
	raw, err := os.ReadFile(inventoryPath)
	require.NoError(t, err, "vendored route inventory missing; copy it from core")

	var inv routeInventory
	require.NoError(t, json.Unmarshal(raw, &inv))
	require.NotEmpty(t, inv.Routes, "inventory is empty")
	require.Equal(t, inv.Count, len(inv.Routes), "inventory count disagrees with its own list")
	return inv
}

// matches reports whether a concrete request path fits a template such as
// /api/mcp_servers/{mcp_server_id}/tools.
func matches(template, actual string) bool {
	pattern := regexp.QuoteMeta(template)
	pattern = regexp.MustCompile(`\\\{[^}]+\\\}`).ReplaceAllString(pattern, `[^/]+`)
	return regexp.MustCompile("^" + pattern + "$").MatchString(actual)
}

// recordingClient returns a client pointed at a server that records the path and
// method of whatever it is asked, and answers blandly so the call completes.
func recordingClient(t *testing.T, seen *[]string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*seen = append(*seen, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	return NewClient(&Config{URL: srv.URL, MaxRetries: 1})
}

// exercise calls every client method that performs HTTP. A method missing here
// is not checked, so the coverage test below fails when one is added.
func exercise(ctx context.Context, c *Client) {
	_, _ = c.GetMCPServerTools(ctx, "srv", "ns")
	_, _ = c.GetMCPServerHealth(ctx, "srv", "ns")
	_ = c.SetL7Policy(ctx, "srv", &L7PolicyPayload{})
	_ = c.ClearL7Policy(ctx, "srv")
	_ = c.DeregisterMCPServer(ctx, "srv", "ns")
	_ = c.Ping(ctx)
}

func TestClientURLsExistInCoreInventory(t *testing.T) {
	inv := loadInventory(t)

	var seen []string
	client := recordingClient(t, &seen)
	exercise(context.Background(), client)

	require.NotEmpty(t, seen, "no requests were recorded; exercise() is not calling anything")

	for _, entry := range seen {
		parts := strings.SplitN(entry, " ", 2)
		method, path := parts[0], parts[1]

		if reason, ok := nonAPIRoutes[path]; ok {
			t.Logf("%s is outside the /api inventory: %s", path, reason)
			continue
		}

		// One path can appear under several entries (l7_policy is DELETE in one
		// and POST,PUT in another), so collect every match before judging the
		// method. Stopping at the first was a bug in this test, and it reported
		// a client fault that did not exist.
		found := false
		methodAllowed := false
		var servedMethods []string
		for _, route := range inv.Routes {
			if !matches(route.Path, path) {
				continue
			}
			found = true
			servedMethods = append(servedMethods, route.Methods...)
			for _, m := range route.Methods {
				if m == method {
					methodAllowed = true
				}
			}
		}
		if found {
			assert.True(t, methodAllowed,
				"core serves %s with %v, but the client uses %s", path, servedMethods, method)
		}
		assert.True(t, found,
			"the client requests %s %s, which core does not serve. "+
				"This is the shape of #91: check the path against %s, and if core moved it, follow.",
			method, path, inventoryPath)
	}
}

func TestEveryHTTPMethodIsExercised(t *testing.T) {
	// Guards the guard. A new client method that is not added to exercise()
	// is silently unchecked, which would let the next wrong path through.
	source, err := os.ReadFile("client.go")
	require.NoError(t, err)

	// Anchored to the start of a line: client.go documents the observe() helper
	// with a commented `func (c *Client) Foo(...)` example, and an unanchored
	// pattern happily reports Foo as an unchecked method.
	decl := regexp.MustCompile(`(?m)^func \(c \*Client\) ([A-Z]\w*)\(`)
	locs := decl.FindAllStringSubmatchIndex(string(source), -1)
	require.NotEmpty(t, locs)

	// A method builds a URL when fmt.Sprintf("%s/... appears before the next
	// declaration -- scoped per method rather than lazily across the file.
	urlBuilders := map[string]bool{}
	for i, loc := range locs {
		name := string(source)[loc[2]:loc[3]]
		end := len(source)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		if strings.Contains(string(source)[loc[0]:end], `fmt.Sprintf("%s/`) {
			urlBuilders[name] = true
		}
	}

	exerciseSrc, err := os.ReadFile("route_contract_test.go")
	require.NoError(t, err)
	body := string(exerciseSrc)
	start := strings.Index(body, "func exercise(")
	require.NotEqual(t, -1, start)
	body = body[start:]

	for name := range urlBuilders {
		assert.Contains(t, body, "c."+name+"(",
			"%s builds a URL but exercise() never calls it, so its path is unchecked", name)
	}
}

func TestTheInventoryStillDescribesTheRoutesWeDependOn(t *testing.T) {
	// Named explicitly so that if core drops one, this fails here with the
	// reason rather than in a cluster with a Degraded CR.
	inv := loadInventory(t)

	required := []string{
		"/api/mcp_servers/{mcp_server_id}/health",
		"/api/mcp_servers/{mcp_server_id}/tools",
		"/api/mcp_servers/{mcp_server_id}/l7_policy",
	}

	paths := map[string]bool{}
	for _, r := range inv.Routes {
		paths[r.Path] = true
	}
	for _, want := range required {
		assert.True(t, paths[want], "core no longer publishes %s, which this operator depends on", want)
	}
}

func TestNoV1PrefixRemains(t *testing.T) {
	source, err := os.ReadFile("client.go")
	require.NoError(t, err)

	// Comments explaining the removal are fine; URL literals are not.
	literals := regexp.MustCompile(`fmt\.Sprintf\("%s/api/v1/`).FindAllString(string(source), -1)

	assert.Empty(t, literals, "the /api/v1 prefix is back in a URL literal")
}
