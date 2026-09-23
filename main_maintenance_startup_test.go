package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestMaintenanceStartupPrecedesSiteWriters(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	positions := map[string]token.Pos{}
	var start token.Pos
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		positions[sel.Sel.Name] = call.Pos()
		if sel.Sel.Name == "Start" {
			if receiver, ok := sel.X.(*ast.CallExpr); ok {
				if factory, ok := receiver.Fun.(*ast.SelectorExpr); ok && factory.Sel.Name == "DefaultMaintenanceManager" {
					start = call.Pos()
				}
			}
		}
		return true
	})
	if start == token.NoPos || positions["RunUpgrades"] >= start {
		t.Fatal("recovery must follow schema upgrades")
	}
	for _, name := range []string{"AutoDeployPluginUpdates", "ReconcilePending", "ResetStuckImageOptimizationJobs", "NewWPInventoryWorker", "NewWPInventoryScheduler", "StartRemoteBackupMaintenanceScheduler"} {
		if positions[name] == token.NoPos || positions[name] <= start {
			t.Errorf("%s must follow synchronous maintenance recovery", name)
		}
	}
	// Ensure the recovery is guarded by the shared main-service mode decision.
	guarded := false
	ast.Inspect(file, func(node ast.Node) bool {
		branch, ok := node.(*ast.IfStmt)
		if !ok || !(branch.Body.Pos() < start && start < branch.Body.End()) {
			return true
		}
		id, ok := branch.Cond.(*ast.Ident)
		guarded = ok && id.Name == "mainServiceMode"
		return true
	})
	if !guarded {
		t.Fatal("CLI tasks must not start restart recovery")
	}
}
