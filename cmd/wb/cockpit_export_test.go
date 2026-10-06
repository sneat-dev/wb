package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestCockpitExportReachesNoStartPath binds the actual export defaults and
// known receiver callbacks below the CLI. Local is a deferred sibling operation:
// its defaults must be constructed only inside that callback, never by the parent.
func TestCockpitExportReachesNoStartPath(t *testing.T) {
	t.Parallel()
	type declaration struct {
		function *ast.FuncDecl
		prefix   string
		imports  map[string]string
	}
	bodies := map[string][]declaration{}
	prefixes := map[string]string{
		"github.com/sneat-dev/wb/internal/cli/cmdcockpit": "cmdcockpit.",
		"github.com/sneat-dev/wb/internal/cockpitrun":     "cockpitrun.",
		"github.com/sneat-dev/wb/internal/daemonruntime":  "daemonruntime.",
		"github.com/sneat-dev/wb/internal/daemon":         "daemon.",
		"github.com/sneat-dev/wb/internal/cli/shared":     "shared.",
	}
	// This finite table is the existing export authority, not a general callgraph.
	for _, source := range []struct{ pattern, prefix string }{
		{"cockpit.go", ""}, {"../../internal/cli/cmdcockpit/*.go", "cmdcockpit."},
		{"../../internal/cockpitrun/*.go", "cockpitrun."},
		{"../../internal/daemonruntime/*.go", "daemonruntime."},
		{"../../internal/daemon/lifecycle.go", "daemon."},
		{"../../internal/cli/shared/format.go", "shared."},
	} {
		files, err := filepath.Glob(source.pattern)
		if err != nil || len(files) == 0 {
			t.Fatalf("missing source %s: %v", source.pattern, err)
		}
		for _, path := range files {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			aliases := map[string]string{}
			for _, spec := range file.Imports {
				value, err := strconv.Unquote(spec.Path.Value)
				if err != nil {
					t.Fatal(err)
				}
				if prefix, ok := prefixes[value]; ok {
					alias := filepath.Base(value)
					if spec.Name != nil {
						alias = spec.Name.Name
					}
					aliases[alias] = prefix
				}
			}
			for _, item := range file.Decls {
				function, ok := item.(*ast.FuncDecl)
				if !ok || function.Body == nil {
					continue
				}
				key := source.prefix + function.Name.Name
				if function.Recv != nil {
					if len(function.Recv.List) != 1 {
						t.Fatalf("ambiguous receiver %s", key)
					}
					expr := function.Recv.List[0].Type
					if star, ok := expr.(*ast.StarExpr); ok {
						expr = star.X
					}
					receiver, ok := expr.(*ast.Ident)
					if !ok {
						t.Fatalf("unresolved receiver %s", key)
					}
					key = source.prefix + receiver.Name + "." + function.Name.Name
				}
				bodies[key] = append(bodies[key], declaration{function, source.prefix, aliases})
			}
		}
	}
	for _, required := range []string{"newCockpitCmd", "newCockpitDependencies", "cmdcockpit.New", "cmdcockpit.newExportCmd", "cockpitrun.DefaultExportDependencies", "cockpitrun.Export", "cockpitrun.cockpitExportGet", "cockpitrun.exportClient", "daemonruntime.NewController", "daemonruntime.Controller.LoadState", "daemon.Store.Load", "daemonruntime.ProcessAlive", "daemonruntime.defaultNativeOperations", "daemonruntime.nativeOperations.processAlive", "daemonruntime.nativeOperations.launchdPID", "shared.RequireOutputFormat"} {
		if len(bodies[required]) == 0 {
			t.Fatalf("required actual declaration missing: %s", required)
		}
	}
	// The LoadState callback follows the constructor's actual store.Load field,
	// not an inferred receiver graph or arbitrary function-valued field.
	storeLoad := false
	ast.Inspect(bodies["daemonruntime.NewController"][0].function.Body, func(n ast.Node) bool {
		pair, ok := n.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		key, ok := pair.Key.(*ast.Ident)
		if !ok || key.Name != "load" {
			return true
		}
		field, ok := pair.Value.(*ast.SelectorExpr)
		if !ok {
			t.Fatal("controller load is no longer the concrete store callback")
		}
		owner, ok := field.X.(*ast.Ident)
		storeLoad = ok && owner.Name == "store" && field.Sel.Name == "Load"
		return true
	})
	if !storeLoad {
		t.Fatal("constructor no longer binds state.load to store.Load")
	}
	// New registers the actual export child. Do not confuse its ordinary open RunE
	// with the child execution path, and do not treat a deferred Local as a call.
	registered := false
	ast.Inspect(bodies["cmdcockpit.New"][0].function.Body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if name, ok := call.Fun.(*ast.Ident); ok && name.Name == "newExportCmd" {
				registered = true
			}
		}
		return true
	})
	if !registered {
		t.Fatal("parent no longer registers the actual export constructor")
	}
	localDefault := false
	ast.Inspect(bodies["newCockpitDependencies"][0].function.Body, func(n ast.Node) bool {
		pair, ok := n.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		key, ok := pair.Key.(*ast.Ident)
		if !ok || key.Name != "Local" {
			return true
		}
		closure, ok := pair.Value.(*ast.FuncLit)
		if !ok {
			t.Fatal("Local must remain a deferred closure")
		}
		ast.Inspect(closure.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if target, ok := call.Fun.(*ast.SelectorExpr); ok && target.Sel.Name == "DefaultDependencies" {
				localDefault = true
			}
			return true
		})
		return false
	})
	if !localDefault {
		t.Fatal("Local no longer constructs its real defaults lazily")
	}
	forbidden := map[string]bool{"Start": true, "StartWithProgress": true, "Stop": true, "Restart": true, "Replace": true, "DefaultDependencies": true, "LocalHTTPClient": true, "NewLocalService": true, "requestCockpitLogin": true, "openBrowser": true, "startDaemonProcess": true, "stopDaemonProcess": true, "launchdBootstrap": true, "WriteFile": true, "MkdirAll": true, "Remove": true, "Rename": true}
	// Explicit receiver callback edges: constructor state.load is store.Load;
	// liveness is native.processAlive, with its platform-only read authority.
	receiverEdges := map[string]string{"LoadState": "daemonruntime.Controller.LoadState", "processAlive": "daemonruntime.nativeOperations.processAlive", "launchdPID": "daemonruntime.nativeOperations.launchdPID"}
	// Builtins are language operations, not unresolved package authorities.
	// Inspect their arguments normally; a same-package declaration still wins.
	builtins := map[string]bool{
		"append": true, "cap": true, "clear": true, "close": true,
		"complex": true, "copy": true, "delete": true, "imag": true,
		"len": true, "make": true, "max": true, "min": true, "new": true,
		"panic": true, "print": true, "println": true, "real": true, "recover": true,
	}
	// Calls with these predeclared type names are conversions. Composite
	// conversions (for example []byte(value)) have non-identifier call targets;
	// their arguments are still visited by the same AST traversal.
	predeclaredTypes := map[string]bool{
		"bool": true, "byte": true, "rune": true, "string": true,
		"int": true, "int8": true, "int16": true, "int32": true, "int64": true,
		"uint": true, "uint8": true, "uint16": true, "uint32": true, "uint64": true,
		"uintptr": true, "float32": true, "float64": true,
		"complex64": true, "complex128": true, "any": true, "error": true,
	}
	// Export's sole local call target is its directly declared fail literal.
	// Require the binding and its signature, reject reassignment/escape/shadowing,
	// and leave the literal body and every call argument in the normal traversal.
	exportBody := bodies["cockpitrun.Export"][0].function.Body
	var failBinding *ast.Ident
	for _, statement := range exportBody.List {
		assignment, ok := statement.(*ast.AssignStmt)
		if !ok || len(assignment.Lhs) != 1 {
			continue
		}
		identifier, ok := assignment.Lhs[0].(*ast.Ident)
		if !ok || identifier.Name != "fail" {
			continue
		}
		if failBinding != nil || assignment.Tok != token.DEFINE || len(assignment.Rhs) != 1 {
			t.Fatal("Export fail binding is ambiguous or reassigned")
		}
		literal, ok := assignment.Rhs[0].(*ast.FuncLit)
		if !ok || literal.Body == nil || literal.Type.Params == nil || literal.Type.Results == nil || len(literal.Type.Params.List) != 1 || len(literal.Type.Results.List) != 1 {
			t.Fatal("Export fail must remain a directly bound single-input/result literal")
		}
		parameter, parameterOK := literal.Type.Params.List[0].Type.(*ast.Ident)
		result, resultOK := literal.Type.Results.List[0].Type.(*ast.Ident)
		if !parameterOK || parameter.Name != "ExportFailure" || len(literal.Type.Params.List[0].Names) != 1 || !resultOK || result.Name != "ExportResult" {
			t.Fatal("Export fail literal signature changed")
		}
		failBinding = identifier
	}
	if failBinding == nil || failBinding.Obj == nil {
		t.Fatal("Export fail literal binding missing")
	}
	allowedFailReferences := map[*ast.Ident]bool{failBinding: true}
	ast.Inspect(exportBody, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if target, ok := call.Fun.(*ast.Ident); ok && target.Name == "fail" {
				if target.Obj != failBinding.Obj || len(call.Args) != 1 || call.Ellipsis.IsValid() {
					t.Fatal("Export fail call has ambiguous binding or arguments")
				}
				allowedFailReferences[target] = true
			}
		}
		return true
	})
	ast.Inspect(exportBody, func(n ast.Node) bool {
		if identifier, ok := n.(*ast.Ident); ok && identifier.Name == "fail" && !allowedFailReferences[identifier] {
			t.Fatal("Export fail literal is reassigned, shadowed or escapes its direct calls")
		}
		return true
	})
	// Follow Alive only when its actual default function value is bound and
	// Export invokes that precise dependency parameter; it is not another root.
	defaultExport := bodies["cockpitrun.DefaultExportDependencies"][0]
	aliveBindings := 0
	clientBindings := 0
	ast.Inspect(defaultExport.function.Body, func(n ast.Node) bool {
		pair, ok := n.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		field, ok := pair.Key.(*ast.Ident)
		if !ok {
			return true
		}
		if field.Name == "Client" {
			factory, ok := pair.Value.(*ast.Ident)
			if !ok || factory.Name != "exportClient" || factory.Obj == nil || factory.Obj.Decl != bodies["cockpitrun.exportClient"][0].function {
				t.Fatal("export Client default no longer binds its concrete HTTP factory")
			}
			clientBindings++
			return true
		}
		if field.Name != "Alive" {
			return true
		}
		value, ok := pair.Value.(*ast.SelectorExpr)
		if !ok {
			t.Fatal("export Alive default is no longer the native function value")
		}
		owner, ok := value.X.(*ast.Ident)
		if !ok || defaultExport.imports[owner.Name] != "daemonruntime." || value.Sel.Name != "ProcessAlive" {
			t.Fatal("export Alive default no longer binds daemonruntime.ProcessAlive")
		}
		aliveBindings++
		return true
	})
	if aliveBindings != 1 || clientBindings != 1 {
		t.Fatal("export must bind exactly one native Alive and concrete Client default")
	}
	var exportDeps *ast.Ident
	for _, parameter := range bodies["cockpitrun.Export"][0].function.Type.Params.List {
		for _, identifier := range parameter.Names {
			if identifier.Name == "deps" {
				exportDeps = identifier
			}
		}
	}
	if exportDeps == nil || exportDeps.Obj == nil {
		t.Fatal("Export dependency parameter missing")
	}
	ast.Inspect(exportBody, func(n ast.Node) bool {
		if assignment, ok := n.(*ast.AssignStmt); ok {
			for _, lhs := range assignment.Lhs {
				if identifier, ok := lhs.(*ast.Ident); ok && identifier.Obj == exportDeps.Obj {
					t.Fatal("Export reassigns its dependency parameter")
				}
			}
		}
		return true
	})
	reached := map[string]bool{}
	queue := []string{"newCockpitCmd", "cmdcockpit.New", "newCockpitDependencies", "cmdcockpit.newExportCmd", "cockpitrun.DefaultExportDependencies", "daemon.Store.Load"}
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		if reached[name] {
			continue
		}
		reached[name] = true
		declarations := bodies[name]
		if len(declarations) == 0 {
			t.Fatalf("missing reached declaration %s", name)
		}
		for _, entry := range declarations {
			ast.Inspect(entry.function.Body, func(n ast.Node) bool {
				if name == "cmdcockpit.New" {
					if _, deferred := n.(*ast.FuncLit); deferred {
						return false
					}
				}
				if pair, ok := n.(*ast.KeyValueExpr); ok && name == "newCockpitDependencies" {
					if key, ok := pair.Key.(*ast.Ident); ok && (key.Name == "Local" || key.Name == "Open" || key.Name == "IsTerminal" || key.Name == "HostedURL") {
						return false
					}
				}
				if assignment, ok := n.(*ast.AssignStmt); ok && name == "daemonruntime.defaultNativeOperations" && len(assignment.Lhs) == 1 && len(assignment.Rhs) == 1 {
					if field, ok := assignment.Lhs[0].(*ast.SelectorExpr); ok && (field.Sel.Name == "runSystemctl" || field.Sel.Name == "runLaunchctl") {
						if _, ok := assignment.Rhs[0].(*ast.FuncLit); ok {
							return false
						}
					}
				}
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				key := ""
				symbol := ""
				switch target := call.Fun.(type) {
				case *ast.Ident:
					symbol = target.Name
					key = entry.prefix + symbol
					if name == "cockpitrun.Export" && target.Name == "fail" && allowedFailReferences[target] {
						key = ""
					}
					if _, declared := bodies[key]; !declared && (builtins[symbol] || predeclaredTypes[symbol]) {
						key = ""
					}
				case *ast.SelectorExpr:
					symbol = target.Sel.Name
					if owner, ok := target.X.(*ast.Ident); ok {
						if name == "cockpitrun.Export" && symbol == "Alive" {
							if owner.Obj != exportDeps.Obj || len(call.Args) != 1 || call.Ellipsis.IsValid() {
								t.Fatal("Export Alive invocation no longer uses its bound dependency")
							}
							key = "daemonruntime.ProcessAlive"
						} else if name == "cockpitrun.Export" && symbol == "Client" {
							if owner.Obj != exportDeps.Obj || len(call.Args) != 0 || call.Ellipsis.IsValid() {
								t.Fatal("Export Client invocation no longer uses its bound dependency")
							}
							key = "cockpitrun.exportClient"
						} else if prefix, ok := entry.imports[owner.Name]; ok {
							key = prefix + symbol
						} else if known, ok := receiverEdges[symbol]; ok {
							key = known
						}
					}
				}
				// ProcessAlive calls the method on the actual freshly constructed
				// native operations value, rather than on a named receiver variable.
				if name == "daemonruntime.ProcessAlive" && symbol == "processAlive" {
					target := call.Fun.(*ast.SelectorExpr)
					constructor, ok := target.X.(*ast.CallExpr)
					if !ok || len(constructor.Args) != 0 || constructor.Ellipsis.IsValid() {
						t.Fatal("ProcessAlive native receiver is no longer its default constructor")
					}
					function, ok := constructor.Fun.(*ast.Ident)
					if !ok || function.Name != "defaultNativeOperations" {
						t.Fatal("ProcessAlive changed its native receiver constructor")
					}
					key = "daemonruntime.nativeOperations.processAlive"
				}
				if key == "" && name == "cockpitrun.DefaultExportDependencies" && symbol == "LoadState" {
					key = "daemonruntime.Controller.LoadState"
				}
				if name == "cmdcockpit.newExportCmd" && symbol == "export" {
					key = ""
				} // concrete operation bound by root Export closure above
				if forbidden[symbol] {
					t.Errorf("%s invokes forbidden %s", name, symbol)
				}
				if symbol == "runLaunchctl" && name == "daemonruntime.nativeOperations.launchdPID" {
					if len(call.Args) == 0 {
						t.Fatal("liveness omitted print")
					}
					literal, ok := call.Args[0].(*ast.BasicLit)
					if !ok || literal.Value != `"print"` {
						t.Fatal("liveness command is no longer read-only print")
					}
				}
				if _, exists := bodies[key]; exists {
					queue = append(queue, key)
				} else if key != "" && (strings.HasPrefix(key, "cockpitrun.") || strings.HasPrefix(key, "cmdcockpit.") || strings.HasPrefix(key, "shared.") || strings.HasPrefix(key, "daemonruntime.")) {
					t.Fatalf("unresolved authority entry %s from %s", key, name)
				}
				return true
			})
		}
	}
	for _, entry := range bodies["daemonruntime.nativeOperations.launchdPID"] {
		calls := 0
		ast.Inspect(entry.function.Body, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if target, ok := call.Fun.(*ast.SelectorExpr); ok && target.Sel.Name == "runLaunchctl" {
					calls++
				}
			}
			return true
		})
		if calls != 1 {
			t.Errorf("launchdPID calls native.runLaunchctl %d times, want exactly one", calls)
		}
	}
	for _, required := range []string{"cockpitrun.Export", "cockpitrun.cockpitExportGet", "cockpitrun.exportClient", "daemonruntime.NewController", "daemonruntime.Controller.LoadState", "daemon.Store.Load", "daemonruntime.ProcessAlive", "daemonruntime.defaultNativeOperations", "daemonruntime.nativeOperations.processAlive", "daemonruntime.nativeOperations.launchdPID", "shared.RequireOutputFormat"} {
		if !reached[required] {
			t.Errorf("actual no-start closure never reached %s", required)
		}
	}
}
