package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestIsMainServiceMode(t *testing.T) {
	tests := []struct {
		name  string
		flags startupFlags
		want  bool
	}{
		{name: "main service", want: true},
		{name: "refresh whitelist", flags: startupFlags{refreshWhitelist: true}},
		{name: "unban all", flags: startupFlags{unbanAll: true}},
		{name: "reset admin", flags: startupFlags{resetAdmin: true}},
		{name: "reset password", flags: startupFlags{resetPassword: true}},
		{name: "file backup", flags: startupFlags{fileBackup: true}},
		{name: "run auto backup", flags: startupFlags{runAutoBackup: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isMainServiceMode(tt.flags); got != tt.want {
				t.Fatalf("isMainServiceMode(%+v) = %v, want %v", tt.flags, got, tt.want)
			}
		})
	}
}

func TestMainServiceOnlyStartupStepsAreGuarded(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"AutoDeployPluginUpdates":         false,
		"EnsurePHPExifExtension":          false,
		"EnsureImageBatchBinaries":        false,
		"ReconcilePending":                false,
		"ResetStuckImageOptimizationJobs": false,
		"FinalizePendingPanelUpdate":      false,
		"RefreshEnabledHandoffs":          false,
	}
	ast.Inspect(file, func(node ast.Node) bool {
		branch, ok := node.(*ast.IfStmt)
		if !ok {
			return true
		}
		id, ok := branch.Cond.(*ast.Ident)
		if !ok || id.Name != "mainServiceMode" {
			return true
		}
		ast.Inspect(branch.Body, func(n ast.Node) bool {
			if selector, ok := n.(*ast.SelectorExpr); ok {
				if _, exists := want[selector.Sel.Name]; exists {
					want[selector.Sel.Name] = true
				}
			}
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch fun := call.Fun.(type) {
			case *ast.SelectorExpr:
				if _, exists := want[fun.Sel.Name]; exists {
					want[fun.Sel.Name] = true
				}
			case *ast.Ident:
				if _, exists := want[fun.Name]; exists {
					want[fun.Name] = true
				}
			}
			return true
		})
		return true
	})
	for name, found := range want {
		if !found {
			t.Errorf("%s must be inside the main-service-only startup block", name)
		}
	}
}
