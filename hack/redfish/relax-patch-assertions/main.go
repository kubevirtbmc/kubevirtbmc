// Command relax-patch-assertions removes the OpenAPI generator's
// "<Type>Required" assertion from every DefaultAPIController PATCH handler
// in the generated api_default.go.
//
// Redfish PATCH bodies are partial updates by design: a client sends only
// the fields it wants to change. openapi-generator's go-server template
// doesn't know that and asserts every field the schema marks as "required"
// is present, which rejects every legitimate partial PATCH. The
// "<Type>Constraints" assertion generated alongside it (which validates the
// value/format of whatever fields *are* present) is correct for PATCH and
// is left untouched.
//
// This was previously a one-off hand-edit to api_default.go, protected from
// regeneration via .openapi-generator-ignore. That only worked as long as
// api_default.go was never regenerated at all; trimming the spec to the
// implemented operation set (see ../trim-redfish-spec) requires
// api_default.go to regenerate every time, so the fix now has to be
// reapplied automatically instead.
//
// Usage (invoked from hack/redfish/generate.sh, after openapi-generator):
//
//	go run ./hack/redfish/relax-patch-assertions \
//	    -file pkg/generated/redfish/server/api_default.go
package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"strings"
)

func main() {
	path := flag.String("file", "pkg/generated/redfish/server/api_default.go", "generated file to fix up in place")
	flag.Parse()

	if err := run(*path); err != nil {
		fmt.Fprintf(os.Stderr, "relax-patch-assertions: %v\n", err)
		os.Exit(1)
	}
}

func run(path string) error {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}

	removed := 0
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || !isDefaultAPIControllerPatchHandler(fn) {
			continue
		}
		removed += stripRequiredAssertions(fn)
	}

	if removed == 0 {
		fmt.Fprintf(os.Stderr, "relax-patch-assertions: no required-field assertions found in PATCH handlers in %s\n", path)
		return nil
	}

	out, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer out.Close() //nolint:errcheck

	if err := format.Node(out, fset, f); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	fmt.Fprintf(os.Stderr, "relax-patch-assertions: removed %d required-field assertion(s) from PATCH handlers -> %s\n", removed, path)
	return nil
}

// isDefaultAPIControllerPatchHandler reports whether fn is a
// (*DefaultAPIController) method whose name follows openapi-generator's
// convention of suffixing the HTTP method onto the operation name, e.g.
// RedfishV1SystemsComputerSystemIdPatch.
func isDefaultAPIControllerPatchHandler(fn *ast.FuncDecl) bool {
	if fn.Recv == nil || len(fn.Recv.List) != 1 || !strings.HasSuffix(fn.Name.Name, "Patch") {
		return false
	}
	star, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	ident, ok := star.X.(*ast.Ident)
	return ok && ident.Name == "DefaultAPIController"
}

// stripRequiredAssertions removes every top-level "if err :=
// AssertXxxRequired(...); err != nil { ... }" statement from fn's body and
// reports how many it removed.
func stripRequiredAssertions(fn *ast.FuncDecl) int {
	removed := 0
	body := fn.Body.List[:0]
	for _, stmt := range fn.Body.List {
		if isRequiredAssertionGuard(stmt) {
			removed++
			continue
		}
		body = append(body, stmt)
	}
	fn.Body.List = body
	return removed
}

// isRequiredAssertionGuard reports whether stmt is a guard clause whose
// condition calls a generated AssertXxxRequired function.
func isRequiredAssertionGuard(stmt ast.Stmt) bool {
	ifStmt, ok := stmt.(*ast.IfStmt)
	if !ok || ifStmt.Init == nil {
		return false
	}
	assign, ok := ifStmt.Init.(*ast.AssignStmt)
	if !ok || len(assign.Rhs) != 1 {
		return false
	}
	call, ok := assign.Rhs[0].(*ast.CallExpr)
	if !ok {
		return false
	}
	ident, ok := call.Fun.(*ast.Ident)
	if !ok {
		return false
	}
	return strings.HasPrefix(ident.Name, "Assert") && strings.HasSuffix(ident.Name, "Required")
}
