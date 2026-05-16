// Package forbidsecretlog provides a go/analysis analyzer that flags
// slog.* calls whose arguments include a selector ending in `.Pass` on
// a config.LabBridgeConfig receiver.
package forbidsecretlog

import (
	"go/ast"
	"go/types"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
)

var Analyzer = &analysis.Analyzer{
	Name:     "forbidsecretlog",
	Doc:      "reports slog.* calls that include config.LabBridgeConfig.Pass",
	Requires: []*analysis.Analyzer{inspect.Analyzer},
	Run:      run,
}

func run(pass *analysis.Pass) (interface{}, error) {
	insp := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)
	filter := []ast.Node{(*ast.CallExpr)(nil)}
	insp.Preorder(filter, func(n ast.Node) {
		call, _ := n.(*ast.CallExpr)
		if !isSlogCall(pass, call) {
			return
		}
		for _, arg := range call.Args {
			sel, ok := arg.(*ast.SelectorExpr)
			if !ok {
				continue
			}
			if sel.Sel.Name != "Pass" {
				continue
			}
			if !isSecretConfigField(pass, sel) {
				continue
			}
			pass.ReportRangef(arg, "logged secret: config secret field passed to slog")
		}
	})
	return nil, nil
}

func isSlogCall(pass *analysis.Pass, call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	x, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	obj, ok := pass.TypesInfo.Uses[x].(*types.PkgName)
	if !ok {
		return false
	}
	return obj.Imported().Path() == "log/slog"
}

func isSecretConfigField(pass *analysis.Pass, sel *ast.SelectorExpr) bool {
	tv, ok := pass.TypesInfo.Types[sel.X]
	if !ok {
		return false
	}
	named, ok := tv.Type.(*types.Named)
	if !ok {
		return false
	}
	name := named.Obj().Name()
	pkg := named.Obj().Pkg()
	if pkg == nil {
		return false
	}
	if pkg.Path() != "github.com/bioexperiment-lab-devices/serialhop/internal/config" {
		return false
	}
	return name == "LabBridgeConfig"
}
