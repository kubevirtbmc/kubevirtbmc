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

func TestHasMapBodyParam(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want bool
	}{
		{"free-form map body", `func (c *DefaultAPIController) FooPost(w http.ResponseWriter, r *http.Request) {
	var bodyParam map[string]interface{}
	_ = bodyParam
}`, true},
		{"typed body", `func (c *DefaultAPIController) BarPost(w http.ResponseWriter, r *http.Request) {
	var insertMediaRequestBodyParam VirtualMediaV163InsertMediaRequestBody
	_ = insertMediaRequestBodyParam
}`, false},
		{"no body", `func (c *DefaultAPIController) BazGet(w http.ResponseWriter, r *http.Request) {}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasMapBodyParam(parseFuncDecl(t, tc.src)); got != tc.want {
				t.Errorf("hasMapBodyParam = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRelaxDecodeGuards(t *testing.T) {
	const handler = `func (c *DefaultAPIController) EjectMediaPost(w http.ResponseWriter, r *http.Request) {
	var bodyParam map[string]interface{}
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(&bodyParam); err != nil {
		c.errorHandler(w, r, &ParsingError{Err: err}, nil)
		return
	}
	result, err := c.service.EjectMediaPost(r.Context(), bodyParam)
	_ = result
	_ = err
}`

	fn := parseFuncDecl(t, handler)
	if patched := relaxDecodeGuards(fn); patched != 1 {
		t.Fatalf("patched %d guards, want 1", patched)
	}

	cond, ok := fn.Body.List[3].(*ast.IfStmt).Cond.(*ast.BinaryExpr)
	if !ok || cond.Op != token.LAND {
		t.Fatalf("decode guard condition was not rewritten into a && expression")
	}

	// Rerunning must be a no-op: the relaxed guard no longer matches the
	// strict "err != nil" shape.
	if patched := relaxDecodeGuards(fn); patched != 0 {
		t.Errorf("second run patched %d guards, want 0 (tool must be idempotent)", patched)
	}
}

// TestRelaxDecodeGuards_LeavesTypedBodies guards against over-matching:
// handlers decoding into a typed model keep their strict guard even though
// the guard shape is identical.
func TestRelaxDecodeGuards_LeavesTypedBodies(t *testing.T) {
	fn := parseFuncDecl(t, `func (c *DefaultAPIController) InsertMediaPost(w http.ResponseWriter, r *http.Request) {
	var insertMediaRequestBodyParam VirtualMediaV163InsertMediaRequestBody
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(&insertMediaRequestBodyParam); err != nil {
		c.errorHandler(w, r, &ParsingError{Err: err}, nil)
		return
	}
}`)
	if patched := relaxDecodeGuards(fn); patched != 0 {
		t.Errorf("patched %d guards in a typed-body handler, want 0", patched)
	}
}

// TestRun_AgainstVendoredFile guards the real, currently-generated
// api_default.go: it runs the tool against a scratch copy and requires the
// two free-form-body action handlers (EjectMedia, SetDefaultBootOrder) to
// end up tolerant while typed-body handlers (Reset, InsertMedia) stay
// strict. If a future openapi-generator version stops emitting free-form
// bodies for these actions, update this test rather than leaving it silently
// unable to prove the fixup still does anything.
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
	content := string(out)

	if got := strings.Count(content, "errors.Is(err, io.EOF)"); got != 2 {
		t.Errorf("found %d relaxed decode guards, want 2 (EjectMedia + SetDefaultBootOrder)", got)
	}
	if !strings.Contains(content, `"io"`) {
		t.Error("io import was not added")
	}
	// Typed-body handlers must keep their strict guards.
	for _, target := range []string{"&computerSystemV1220ResetRequestBodyParam", "&virtualMediaV163InsertMediaRequestBodyParam"} {
		idx := strings.Index(content, target)
		if idx < 0 {
			t.Fatalf("typed-body decode of %s not found; generator output changed?", target)
		}
		guard := content[idx : idx+200]
		if !strings.Contains(guard, "; err != nil {") {
			t.Errorf("typed-body decode guard for %s was relaxed; only free-form map bodies may be relaxed", target)
		}
	}
}

// TestRun_PreservesComments guards against parsing without
// parser.ParseComments, which silently drops every comment in the file --
// including the "Code generated ... DO NOT EDIT." header golangci-lint's
// generated-file exclusion relies on to recognize the file at all.
func TestRun_PreservesComments(t *testing.T) {
	src := `// Code generated by OpenAPI Generator (https://openapi-generator.tech); DO NOT EDIT.

package server

// EjectMediaPost -
func (c *DefaultAPIController) EjectMediaPost(w http.ResponseWriter, r *http.Request) {
	var bodyParam map[string]interface{}
	d := json.NewDecoder(r.Body)
	if err := d.Decode(&bodyParam); err != nil {
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
	if !strings.Contains(string(out), "// EjectMediaPost -") {
		t.Errorf("handler doc comment was dropped:\n%s", out)
	}
}
