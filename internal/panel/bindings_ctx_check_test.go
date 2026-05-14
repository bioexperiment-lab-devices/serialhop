package panel

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// TestBindings_NoMethodTakesContextContext is the cross-platform
// regression test for the silent reachability bug:
//
//	"error parsing arguments: received 0 arguments to method
//	 'main.App.GetX', expected 1"
//
// Wails v2.12.0 does NOT auto-inject context.Context as the first
// argument for methods reached through embedding (main.App embeds
// *panel.App). The JS shim always calls bindings with zero arguments;
// if any (a *App) method binding expects a context.Context, the bridge
// rejects every JS-side invocation, the SPA's promise rejects, and the
// Devices/Ports tabs fall back to their initial reachable=false state
// — which renders as "Can't reach the local service" even though
// every layer below is fine. That confusion ate three speculative
// fixes (#90, #95, #99→#100) before the binding error banner added in
// #100 surfaced the actual Wails error.
//
// Implemented via go/parser rather than reflection because the App
// type lives in a //go:build windows file; an AST walk works on every
// platform's CI runner.
func TestBindings_NoMethodTakesContextContext(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "bindings.go", nil, 0)
	if err != nil {
		t.Fatalf("parse bindings.go: %v", err)
	}

	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Recv == nil || len(fd.Recv.List) == 0 {
			continue
		}
		if !receiverIsApp(fd.Recv.List[0].Type) {
			continue
		}
		if fd.Type.Params == nil {
			continue
		}
		for _, field := range fd.Type.Params.List {
			if isContextContext(field.Type) {
				t.Errorf("App.%s takes context.Context as a parameter — Wails v2.12.0 doesn't auto-inject through embedded methods, so the JS-side bridge will reject every invocation with \"received 0 arguments, expected 1\". Drop the parameter and derive context inside the body via a.callCtx() instead.",
					fd.Name.Name)
				break
			}
		}
	}
}

// receiverIsApp matches `(a *App)` (and the no-name form `(*App)`).
func receiverIsApp(expr ast.Expr) bool {
	star, ok := expr.(*ast.StarExpr)
	if !ok {
		return false
	}
	ident, ok := star.X.(*ast.Ident)
	return ok && ident.Name == "App"
}

// isContextContext matches the qualified identifier `context.Context`.
// It does NOT try to be clever about aliases — every existing binding
// uses the canonical form and the rare alias case is exactly the kind
// of thing this test should still complain about.
func isContextContext(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return pkg.Name == "context" && strings.EqualFold(sel.Sel.Name, "Context")
}
