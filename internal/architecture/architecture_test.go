package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestProductionCodeHasNoForbiddenImports(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", ".."))
	forbidden := map[string]bool{"os/exec": true, "unsafe": true, "plugin": true, "C": true}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() && (entry.Name() == ".git" || entry.Name() == "vendor") {
			return filepath.SkipDir
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Errorf("parse %s: %v", path, err)
			return nil
		}
		for _, spec := range parsed.Imports {
			name, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Errorf("unquote import in %s: %v", path, err)
				continue
			}
			if forbidden[name] {
				t.Errorf("production file %s imports forbidden package %q", path, name)
			}
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			qualifier, ok := selector.X.(*ast.Ident)
			if !ok {
				return true
			}
			forbiddenCall := (qualifier.Name == "os" && selector.Sel.Name == "StartProcess") ||
				(qualifier.Name == "syscall" || qualifier.Name == "unix") &&
					(selector.Sel.Name == "Exec" || selector.Sel.Name == "ForkExec" || selector.Sel.Name == "StartProcess")
			if forbiddenCall {
				t.Errorf("production file %s references forbidden process primitive %s.%s", path, qualifier.Name, selector.Sel.Name)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
