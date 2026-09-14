package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func parseFuncDecl(t *testing.T, src string) *ast.FuncDecl {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "api_default.go", "package server\n"+src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return f.Decls[0].(*ast.FuncDecl)
}

func TestIsDefaultAPIControllerPatchHandler(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want bool
	}{
		{"patch handler", `func (c *DefaultAPIController) RedfishV1SystemsComputerSystemIdPatch(w http.ResponseWriter, r *http.Request) {}`, true},
		{"get handler", `func (c *DefaultAPIController) RedfishV1SystemsComputerSystemIdGet(w http.ResponseWriter, r *http.Request) {}`, false},
		{"patch on other receiver", `func (c *SomeOtherController) FooPatch(w http.ResponseWriter, r *http.Request) {}`, false},
		{"free function ending in Patch", `func StandalonePatch() {}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isDefaultAPIControllerPatchHandler(parseFuncDecl(t, tc.src)); got != tc.want {
				t.Errorf("isDefaultAPIControllerPatchHandler = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestStripRequiredAssertions(t *testing.T) {
	fn := parseFuncDecl(t, `func (c *DefaultAPIController) RedfishV1SystemsComputerSystemIdPatch(w http.ResponseWriter, r *http.Request) {
	params := mux.Vars(r)
	computerSystemIdParam := params["ComputerSystemId"]
	var computerSystemV1220ComputerSystemParam ComputerSystemV1220ComputerSystem
	if err := AssertComputerSystemV1220ComputerSystemRequired(computerSystemV1220ComputerSystemParam); err != nil {
		c.errorHandler(w, r, err, nil)
		return
	}
	if err := AssertComputerSystemV1220ComputerSystemConstraints(computerSystemV1220ComputerSystemParam); err != nil {
		c.errorHandler(w, r, err, nil)
		return
	}
	result, err := c.service.RedfishV1SystemsComputerSystemIdPatch(r.Context(), computerSystemIdParam, computerSystemV1220ComputerSystemParam)
	_ = result
	_ = err
}`)

	removed := stripRequiredAssertions(fn)
	if removed != 1 {
		t.Fatalf("removed %d statements, want 1", removed)
	}

	var b strings.Builder
	for _, stmt := range fn.Body.List {
		b.WriteString(stmtCallName(stmt))
		b.WriteString("\n")
	}
	got := b.String()
	if strings.Contains(got, "Required") {
		t.Errorf("Required assertion survived: %s", got)
	}
	if !strings.Contains(got, "Constraints") {
		t.Errorf("Constraints assertion should have been left in place: %s", got)
	}
	if len(fn.Body.List) != 7 {
		t.Errorf("body has %d statements, want 7 (everything but the removed Required guard)", len(fn.Body.List))
	}
}

// stmtCallName extracts the called function's name from an "if err :=
// F(...); err != nil {...}" statement, for readable test failure output.
func stmtCallName(stmt ast.Stmt) string {
	ifStmt, ok := stmt.(*ast.IfStmt)
	if !ok || ifStmt.Init == nil {
		return ""
	}
	assign, ok := ifStmt.Init.(*ast.AssignStmt)
	if !ok || len(assign.Rhs) != 1 {
		return ""
	}
	call, ok := assign.Rhs[0].(*ast.CallExpr)
	if !ok {
		return ""
	}
	ident, ok := call.Fun.(*ast.Ident)
	if !ok {
		return ""
	}
	return ident.Name
}

func TestIsRequiredAssertionGuard(t *testing.T) {
	fn := parseFuncDecl(t, `func f() {
	if err := AssertFooRequired(x); err != nil {
		return
	}
	if err := AssertFooConstraints(x); err != nil {
		return
	}
	if x == nil {
		return
	}
}`)
	want := []bool{true, false, false}
	for i, stmt := range fn.Body.List {
		if got := isRequiredAssertionGuard(stmt); got != want[i] {
			t.Errorf("statement %d: isRequiredAssertionGuard = %v, want %v", i, got, want[i])
		}
	}
}

// TestRun_AgainstVendoredFile guards the real, currently-generated
// api_default.go: it runs the tool against a scratch copy and requires that
// it find and remove at least the ComputerSystem PATCH assertion. If a
// future openapi-generator version stops emitting an overly strict Required
// assertion for PATCH bodies, update this test rather than leaving it
// silently unable to prove the fixup still does anything.
func TestRun_AgainstVendoredFile(t *testing.T) {
	const vendored = "../../../pkg/generated/redfish/server/api_default.go"
	src, err := os.ReadFile(vendored)
	if err != nil {
		t.Skipf("vendored file not available: %v", err)
	}

	scratch := filepath.Join(t.TempDir(), "api_default.go")
	if err := os.WriteFile(scratch, src, 0o644); err != nil {
		t.Fatalf("write scratch copy: %v", err)
	}

	if err := run(scratch); err != nil {
		t.Fatalf("run: %v", err)
	}

	out, err := os.ReadFile(scratch)
	if err != nil {
		t.Fatalf("read scratch copy: %v", err)
	}
	if strings.Contains(string(out), "AssertComputerSystemV1220ComputerSystemRequired(computerSystemV1220ComputerSystemParam)") {
		t.Error("AssertComputerSystemV1220ComputerSystemRequired guard survived in the PATCH handler")
	}
	if !strings.Contains(string(out), "AssertComputerSystemV1220ComputerSystemConstraints(computerSystemV1220ComputerSystemParam)") {
		t.Error("AssertComputerSystemV1220ComputerSystemConstraints guard should have been left in place")
	}
}

// TestRun_PreservesComments guards against parsing without
// parser.ParseComments, which silently drops every comment in the file --
// including the "Code generated ... DO NOT EDIT." header golangci-lint's
// generated-file exclusion relies on to recognize the file at all.
func TestRun_PreservesComments(t *testing.T) {
	src := `// Code generated by OpenAPI Generator (https://openapi-generator.tech); DO NOT EDIT.

package server

// RedfishV1SystemsComputerSystemIdPatch -
func (c *DefaultAPIController) RedfishV1SystemsComputerSystemIdPatch(w http.ResponseWriter, r *http.Request) {
	if err := AssertComputerSystemV1220ComputerSystemRequired(computerSystemV1220ComputerSystemParam); err != nil {
		c.errorHandler(w, r, err, nil)
		return
	}
}
`
	scratch := filepath.Join(t.TempDir(), "api_default.go")
	if err := os.WriteFile(scratch, []byte(src), 0o644); err != nil {
		t.Fatalf("write scratch file: %v", err)
	}

	if err := run(scratch); err != nil {
		t.Fatalf("run: %v", err)
	}

	out, err := os.ReadFile(scratch)
	if err != nil {
		t.Fatalf("read scratch file: %v", err)
	}
	if !strings.Contains(string(out), "Code generated by OpenAPI Generator") {
		t.Errorf("generated-file header comment was dropped:\n%s", out)
	}
	if !strings.Contains(string(out), "// RedfishV1SystemsComputerSystemIdPatch -") {
		t.Errorf("handler doc comment was dropped:\n%s", out)
	}
}
