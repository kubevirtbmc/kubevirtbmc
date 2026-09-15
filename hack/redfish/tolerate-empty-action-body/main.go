// Command tolerate-empty-action-body rewrites the JSON body-decode guard in
// the generated DefaultAPIController action handlers whose request body is a
// free-form map (map[string]interface{}) so that an empty request body is
// treated as "no parameters supplied" instead of a 400 ParsingError.
//
// Redfish actions without required parameters (VirtualMedia.EjectMedia,
// ComputerSystem.SetDefaultBootOrder) accept an empty or absent JSON object.
// Per the Redfish spec (DSP0266) clients should send "{}", but some clients
// -- notably sushy, used by Ironic/Metal3 -- send a completely empty body
// with no Content-Type. openapi-generator's go-server template
// unconditionally decodes the body, and the resulting io.EOF is turned into
// a 400, which breaks those clients. (sushy additionally fails to parse the
// generator's plain-string error body, masking the 400 behind an
// AttributeError: 'str' object has no attribute 'get'.)
//
// Handlers with a typed request body (VirtualMedia.InsertMedia,
// ComputerSystem.Reset) keep the strict behavior: their schemas carry
// required fields worth enforcing.
//
// Like the PATCH-assertion relaxation (see ../relax-patch-assertions), this
// was previously a one-off hand-edit to api_default.go (#180), protected
// from regeneration via .openapi-generator-ignore. Once api_default.go
// started regenerating every run (#297) the hand-edit was silently
// overwritten, so it now has to be reapplied automatically.
//
// Usage (invoked from hack/redfish/generate.sh, after relax-patch-assertions):
//
//	go run ./hack/redfish/tolerate-empty-action-body \
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
)

func main() {
	path := flag.String("file", "pkg/generated/redfish/server/api_default.go", "generated file to fix up in place")
	flag.Parse()

	if err := run(*path); err != nil {
		fmt.Fprintf(os.Stderr, "tolerate-empty-action-body: %v\n", err)
		os.Exit(1)
	}
}

func run(path string) error {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}

	patched := 0
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || !isDefaultAPIControllerHandler(fn) || !hasMapBodyParam(fn) {
			continue
		}
		patched += relaxDecodeGuards(fn)
	}

	if patched == 0 {
		fmt.Fprintf(os.Stderr, "tolerate-empty-action-body: no strict empty-body decode guards found in %s\n", path)
		return nil
	}

	ensureIOImport(f)

	out, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer out.Close() //nolint:errcheck

	if err := format.Node(out, fset, f); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	fmt.Fprintf(os.Stderr, "tolerate-empty-action-body: relaxed %d decode guard(s) to tolerate empty bodies -> %s\n", patched, path)
	return nil
}

// isDefaultAPIControllerHandler reports whether fn is a method on
// *DefaultAPIController.
func isDefaultAPIControllerHandler(fn *ast.FuncDecl) bool {
	if fn.Recv == nil || len(fn.Recv.List) != 1 {
		return false
	}
	star, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	ident, ok := star.X.(*ast.Ident)
	return ok && ident.Name == "DefaultAPIController"
}

// hasMapBodyParam reports whether fn's body declares
// "var bodyParam map[string]interface{}", i.e. the operation's request body
// is a free-form object with no schema-required fields. Only such handlers
// are safe to treat an absent body as "no parameters".
func hasMapBodyParam(fn *ast.FuncDecl) bool {
	for _, stmt := range fn.Body.List {
		decl, ok := stmt.(*ast.DeclStmt)
		if !ok {
			continue
		}
		gen, ok := decl.Decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || len(vs.Names) != 1 || vs.Names[0].Name != "bodyParam" {
				continue
			}
			if _, ok := vs.Type.(*ast.MapType); ok {
				return true
			}
		}
	}
	return false
}

// relaxDecodeGuards rewrites every "if err := d.Decode(&bodyParam); err !=
// nil {" guard in fn's body into "...; err != nil && !errors.Is(err, io.EOF)
// {" and reports how many it rewrote. Guards already relaxed are skipped, so
// rerunning the tool is a no-op.
func relaxDecodeGuards(fn *ast.FuncDecl) int {
	patched := 0
	for _, stmt := range fn.Body.List {
		ifStmt, ok := stmt.(*ast.IfStmt)
		if !ok || !isBodyParamDecode(ifStmt.Init) {
			continue
		}
		cond, ok := ifStmt.Cond.(*ast.BinaryExpr)
		if !ok || cond.Op != token.NEQ || !isErrNotNil(cond) {
			continue
		}
		ifStmt.Cond = &ast.BinaryExpr{
			X:  cond,
			Op: token.LAND,
			Y: &ast.UnaryExpr{
				Op: token.NOT,
				X: &ast.CallExpr{
					Fun: &ast.SelectorExpr{X: ast.NewIdent("errors"), Sel: ast.NewIdent("Is")},
					Args: []ast.Expr{
						ast.NewIdent("err"),
						&ast.SelectorExpr{X: ast.NewIdent("io"), Sel: ast.NewIdent("EOF")},
					},
				},
			},
		}
		patched++
	}
	return patched
}

// isBodyParamDecode reports whether stmt is an assignment of the form
// "err := d.Decode(&bodyParam)".
func isBodyParamDecode(stmt ast.Stmt) bool {
	assign, ok := stmt.(*ast.AssignStmt)
	if !ok || len(assign.Rhs) != 1 {
		return false
	}
	call, ok := assign.Rhs[0].(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Decode" {
		return false
	}
	arg, ok := call.Args[0].(*ast.UnaryExpr)
	if !ok || arg.Op != token.AND {
		return false
	}
	ident, ok := arg.X.(*ast.Ident)
	return ok && ident.Name == "bodyParam"
}

// isErrNotNil reports whether cond is the binary expression "err != nil".
func isErrNotNil(cond *ast.BinaryExpr) bool {
	x, ok := cond.X.(*ast.Ident)
	if !ok || x.Name != "err" {
		return false
	}
	y, ok := cond.Y.(*ast.Ident)
	return ok && y.Name == "nil"
}

// ensureIOImport adds the "io" import to f's import declaration in
// alphabetical order, unless it is already present.
func ensureIOImport(f *ast.File) {
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.IMPORT {
			continue
		}
		idx := len(gen.Specs)
		for i, spec := range gen.Specs {
			path := spec.(*ast.ImportSpec).Path.Value
			if path == `"io"` {
				return
			}
			if path > `"io"` && idx == len(gen.Specs) {
				idx = i
			}
		}
		gen.Specs = append(gen.Specs, nil)
		copy(gen.Specs[idx+1:], gen.Specs[idx:])
		gen.Specs[idx] = &ast.ImportSpec{Path: &ast.BasicLit{Kind: token.STRING, Value: `"io"`}}
		return
	}
}
