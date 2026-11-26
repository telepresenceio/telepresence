package manager

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
)

// callClass says which session check a gRPC call uses.
type callClass int

const (
	// classInternal: served only on the internal listener; externalService
	// returns internalOnly.
	classInternal callClass = iota
	// classNoSession: the request carries no session to check.
	classNoSession
	// classWithAccess: clientSessionWithAccess, then a filter per workload.
	classWithAccess
	// classStrict: ensureClientSession.
	classStrict
	// classRequestReviewed: ownedClientSession plus a review of the
	// namespace or workload that the request names.
	classRequestReviewed
	// classOpen: an ownership check only; open to every connected client or
	// limited to the caller's own session.
	classOpen
)

type callEntry struct {
	class  callClass
	reason string
}

// callClasses holds the class of every call of the manager service. A new
// call must be added here with the session check that its handler uses.
var callClasses = map[string]callEntry{
	// Internal listener only.
	"GetTelepresenceAPI":     {classInternal, "used by the agents' local API"},
	"ReconnectAgent":         {classInternal, "agent session upkeep"},
	"ArriveAsAgent":          {classInternal, "agent arrival"},
	"GetIntercept":           {classInternal, "used by the traffic-agents"},
	"ReviewIntercept":        {classInternal, "used by the traffic-agents"},
	"AuthorizeSessionAccess": {classInternal, "used by the traffic-agents"},
	"WatchLogLevel":          {classInternal, "used by the traffic-agents"},
	"GetQuicAgentCert":       {classInternal, "used by the traffic-agents"},
	"WatchQuicBackends":      {classInternal, "used by the traffic-agents"},
	"ReportMetrics":          {classInternal, "used by the traffic-agents"},
	// No session.
	"Version":                         {classNoSession, "asked before a session exists"},
	"ArriveAsClient":                  {classNoSession, "creates the session"},
	"GetAgentImageFQN":                {classNoSession, "configuration; the request has no session"},
	"GetClientConfig":                 {classNoSession, "configuration; the request has no session"},
	"WatchAgents":                     {classWithAccess, "filters the agents of the session namespace per workload"},
	"WatchAgentsDelta":                {classWithAccess, "filters the agents of the session namespace per workload"},
	"UninstallAgents":                 {classWithAccess, "reviews each named agent, or the whole namespace"},
	"WatchAgentPods":                  {classRequestReviewed, "reviews the namespaces it watches"},
	"WatchAgentPodsDelta":             {classRequestReviewed, "reviews the namespaces it watches"},
	"WatchAgentPodsInNamespacesDelta": {classRequestReviewed, "reviews the namespaces it watches"},
	"WatchSessionEvents":              {classRequestReviewed, "reviews the namespaces it watches"},
	"WatchWorkloads":                  {classRequestReviewed, "reviews the namespace and filters per workload"},
	"GetAgentConfig":                  {classRequestReviewed, "reviews the workload"},
	"ResolveServicePort":              {classRequestReviewed, "reviews the namespace"},
	"EnsureAgent":                     {classRequestReviewed, "reviews the workload"},
	"PrepareIntercept":                {classRequestReviewed, "reviews the intercept"},
	"CreateIntercept":                 {classRequestReviewed, "reviews the intercept"},
	"StreamLogs":                      {classRequestReviewed, "reviews each namespace it reads logs from"},
	"RemoveIntercept":                 {classOpen, "removes an intercept of the caller's own session"},
	"ReleaseAgent":                    {classOpen, "releases a claim held by the caller's own session"},
	"WatchIntercepts":                 {classOpen, "shows only the caller's own intercepts"},
	"WatchInterceptsDelta":            {classOpen, "shows only the caller's own intercepts"},
	"Remain":                          {classOpen, "session upkeep"},
	"Depart":                          {classOpen, "ends the caller's own session"},
	"ReconnectClient":                 {classOpen, "restores the caller's own session"},
	"Tunnel":                          {classOpen, "network access comes with the connect grant"},
	"GetQuicTunnelEndpoint":           {classOpen, "network access comes with the connect grant"},
	"GetSessionCredential":            {classOpen, "network access comes with the connect grant"},
	"Lookup":                          {classOpen, "name resolution comes with the connect grant"},
	"LookupDNS":                       {classOpen, "name resolution comes with the connect grant"},
	"WatchNamespaces":                 {classOpen, "names of the managed namespaces, needed for name resolution"},
	"WatchClusterInfo":                {classOpen, "routing information that every client needs"},
	"GetKnownWorkloadKinds":           {classOpen, "configuration, no namespace content"},
	"SetLogLevel":                     {classOpen, "sets the log level; decided separately from namespace access"},
}

// expectedChecks maps a class to the session checks its handler calls. An
// open call may also call ownedClientSession.
var expectedChecks = map[callClass][]string{
	classWithAccess:      {"clientSessionWithAccess"},
	classStrict:          {"ensureClientSession"},
	classRequestReviewed: {"ownedClientSession"},
}

var (
	sessionChecks = []string{"ensureClientSession", "clientSessionWithAccess", "ownedClientSession"}

	// requestReviews are the helpers that review the namespace or workload
	// named in a request.
	requestReviews = []string{
		"readAccess", "checkReadAccess", "watchAgentPods", "watchAgentPodsDelta",
		"authorizeEnsureAgent", "authorizeIntercept", "newLogNamespaceAuth",
	}

	// ownershipChecks are the calls through which an open handler verifies
	// that the caller owns the session.
	ownershipChecks = []string{"ownedClientSession", "ClientOwnershipError", "watchIntercepts", "Tunnel"}
)

// TestCallClass keeps callClasses true: it lists every call of the service
// description and compares the class of each with the session check that the
// handler's source calls.
func TestCallClass(t *testing.T) {
	var names []string
	for _, m := range rpc.Manager_ServiceDesc.Methods {
		names = append(names, m.MethodName)
	}
	for _, s := range rpc.Manager_ServiceDesc.Streams {
		names = append(names, s.StreamName)
	}
	for _, name := range names {
		if _, ok := callClasses[name]; !ok {
			t.Errorf("call %s is not classified; add it to the table in service_callclass_test.go with the session check it uses", name)
		}
	}
	for name := range callClasses {
		if !slices.Contains(names, name) {
			t.Errorf("table entry %s is not a call of the manager service; remove it from the table in service_callclass_test.go", name)
		}
	}

	internal := calledNames(t, "externalService")
	handlers := calledNames(t, "service")
	for _, name := range names {
		entry, ok := callClasses[name]
		if !ok {
			continue
		}
		t.Run(name, func(t *testing.T) {
			assert.NotEmpty(t, entry.reason, "give call %s a reason in the table", name)
			ext, ok := internal[name]
			require.True(t, ok, "externalService has no method %s", name)
			if entry.class == classInternal {
				assert.Contains(t, ext, "internalOnly",
					"call %s is classified internal only, but externalService.%s does not return internalOnly", name, name)
				return
			}
			assert.NotContains(t, ext, "internalOnly",
				"externalService.%s returns internalOnly; classify call %s as internal only", name, name)

			calls, ok := handlers[name]
			require.True(t, ok, "service has no method %s", name)
			used := usedSessionChecks(calls)
			if entry.class == classOpen {
				if !hasAny(calls, ownershipChecks) {
					t.Errorf("call %s is classified open, but its handler calls none of %v; check session ownership", name, ownershipChecks)
				}
				used = slices.DeleteFunc(used, func(c string) bool { return c == "ownedClientSession" })
			}
			if entry.class == classRequestReviewed && !hasAny(calls, requestReviews) {
				t.Errorf("call %s is classified request namespace reviewed, but its handler calls none of %v; review the namespace or workload the request names",
					name, requestReviews)
			}
			if want := expectedChecks[entry.class]; !slices.Equal(used, want) {
				t.Errorf("call %s: the table says its session checks are %v, but the handler calls %v; fix the handler or the table in service_callclass_test.go",
					name, want, used)
			}
		})
	}
}

func hasAny(calls, wanted []string) bool {
	return slices.ContainsFunc(wanted, func(w string) bool { return slices.Contains(calls, w) })
}

// usedSessionChecks returns the session checks among calls, in sessionChecks order.
func usedSessionChecks(calls []string) []string {
	var used []string
	for _, c := range sessionChecks {
		if slices.Contains(calls, c) {
			used = append(used, c)
		}
	}
	return used
}

// calledNames parses the non-test files of the package and returns, for each
// method of the pointer type recv, the names of everything its body calls.
func calledNames(t *testing.T, recv string) map[string][]string {
	t.Helper()
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	var files []*ast.File
	for _, e := range entries {
		if name := e.Name(); strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") {
			f, err := parser.ParseFile(fset, name, nil, 0)
			require.NoError(t, err)
			files = append(files, f)
		}
	}
	methods := map[string][]string{}
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || fn.Body == nil || !isPointerTo(fn.Recv.List[0].Type, recv) {
				continue
			}
			methods[fn.Name.Name] = callsIn(fn.Body)
		}
	}
	return methods
}

func isPointerTo(expr ast.Expr, name string) bool {
	star, ok := expr.(*ast.StarExpr)
	if !ok {
		return false
	}
	id, ok := star.X.(*ast.Ident)
	return ok && id.Name == name
}

// callsIn returns the names of the functions and methods that body calls.
func callsIn(body *ast.BlockStmt) []string {
	var names []string
	ast.Inspect(body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			switch f := call.Fun.(type) {
			case *ast.Ident:
				names = append(names, f.Name)
			case *ast.SelectorExpr:
				names = append(names, f.Sel.Name)
			}
		}
		return true
	})
	return names
}
