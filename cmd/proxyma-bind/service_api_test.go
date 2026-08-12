package proxyma_bind

import (
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestGomobileExportedAPIsAreNonVariadic(t *testing.T) {
	t.Parallel()

	for _, fn := range bindingFunctions(t) {
		if ast.IsExported(fn.Name.Name) && isVariadic(fn.Type.Params) {
			t.Errorf("%s is variadic; gobind does not support exported variadic APIs", fn.Name.Name)
		}
	}
}

func TestRunServiceAPISignatures(t *testing.T) {
	t.Parallel()

	funcs := make(map[string]*ast.FuncDecl)
	for _, fn := range bindingFunctions(t) {
		if fn.Recv == nil {
			funcs[fn.Name.Name] = fn
		}
	}

	tests := []struct {
		name       string
		paramCount int
	}{
		{name: "RunService", paramCount: 2},
		{name: "RunServiceWithStrategy", paramCount: 3},
	}
	for _, tt := range tests {
		fn, ok := funcs[tt.name]
		if !ok {
			t.Errorf("missing %s", tt.name)
			continue
		}
		if got := fieldCount(fn.Type.Params); got != tt.paramCount {
			t.Errorf("%s has %d parameters; want %d", tt.name, got, tt.paramCount)
		}
		if isVariadic(fn.Type.Params) {
			t.Errorf("%s must be non-variadic", tt.name)
		}
		if !allFieldsAreStrings(fn.Type.Params) {
			t.Errorf("%s parameters must all be strings", tt.name)
		}
		if fieldCount(fn.Type.Results) != 1 || !allFieldsAreStrings(fn.Type.Results) {
			t.Errorf("%s must return exactly one string", tt.name)
		}
	}
}

func TestRunServiceDocumentsStrategyMigration(t *testing.T) {
	t.Parallel()

	funcs := make(map[string]*ast.FuncDecl)
	for _, fn := range bindingFunctions(t) {
		if fn.Recv == nil {
			funcs[fn.Name.Name] = fn
		}
	}
	runDoc := funcs["RunService"].Doc.Text()
	if !strings.Contains(runDoc, "breaking Go API change") ||
		!strings.Contains(runDoc, "RunServiceWithStrategy") {
		t.Fatalf("RunService migration documentation missing: %q", runDoc)
	}
	strategyDoc := funcs["RunServiceWithStrategy"].Doc.Text()
	if !strings.Contains(strategyDoc, "migration API") {
		t.Fatalf("RunServiceWithStrategy migration note missing: %q", strategyDoc)
	}
}

func bindingFunctions(t *testing.T) []*ast.FuncDecl {
	t.Helper()

	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate binding API test source")
	}
	dir := filepath.Dir(filename)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read binding package directory: %v", err)
	}
	fset := token.NewFileSet()

	var funcs []*ast.FuncDecl
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		matched, err := build.Default.MatchFile(dir, name)
		if err != nil {
			t.Fatalf("evaluate build constraints for %s: %v", name, err)
		}
		if !matched {
			continue
		}
		file, err := parser.ParseFile(
			fset,
			filepath.Join(dir, name),
			nil,
			parser.SkipObjectResolution|parser.ParseComments,
		)
		if err != nil {
			t.Fatalf("parse binding source %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok {
				funcs = append(funcs, fn)
			}
		}
	}
	return funcs
}

func isVariadic(fields *ast.FieldList) bool {
	if fields == nil || len(fields.List) == 0 {
		return false
	}
	_, ok := fields.List[len(fields.List)-1].Type.(*ast.Ellipsis)
	return ok
}

func fieldCount(fields *ast.FieldList) int {
	if fields == nil {
		return 0
	}
	count := 0
	for _, field := range fields.List {
		if len(field.Names) == 0 {
			count++
			continue
		}
		count += len(field.Names)
	}
	return count
}

func allFieldsAreStrings(fields *ast.FieldList) bool {
	if fields == nil {
		return false
	}
	for _, field := range fields.List {
		ident, ok := field.Type.(*ast.Ident)
		if !ok || ident.Name != "string" {
			return false
		}
	}
	return true
}
