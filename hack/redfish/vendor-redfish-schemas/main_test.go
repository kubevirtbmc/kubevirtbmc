package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func mustParse(t *testing.T, src string) *yaml.Node {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return doc.Content[0]
}

func mustDump(t *testing.T, n *yaml.Node) string {
	t.Helper()
	var b strings.Builder
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(n); err != nil {
		t.Fatalf("encode: %v", err)
	}
	enc.Close() //nolint:errcheck
	return b.String()
}

func TestRewriteExternalRefs(t *testing.T) {
	root := mustParse(t, `
schema:
  $ref: http://redfish.dmtf.org/schemas/v1/Resource.yaml#/components/schemas/Resource_Id
local:
  $ref: '#/components/schemas/RedfishError'
nested:
  items:
    $ref: http://redfish.dmtf.org/schemas/v1/odata-v4.yaml#/components/schemas/odata-v4_idRef
`)
	var got []string
	rewriteExternalRefs(root, "schemas/", func(filename string) { got = append(got, filename) })

	out := mustDump(t, root)
	if !strings.Contains(out, "./schemas/Resource.yaml#/components/schemas/Resource_Id") {
		t.Errorf("external ref not rewritten to local relative path:\n%s", out)
	}
	if !strings.Contains(out, "'#/components/schemas/RedfishError'") && !strings.Contains(out, `"#/components/schemas/RedfishError"`) {
		t.Errorf("purely local ref should be left untouched:\n%s", out)
	}
	if !strings.Contains(out, "./schemas/odata-v4.yaml#/components/schemas/odata-v4_idRef") {
		t.Errorf("nested external ref not rewritten:\n%s", out)
	}

	wantFiles := map[string]bool{"Resource.yaml": true, "odata-v4.yaml": true}
	if len(got) != 2 || !wantFiles[got[0]] || !wantFiles[got[1]] {
		t.Errorf("enqueued files = %v, want Resource.yaml and odata-v4.yaml", got)
	}
}

func TestFixRequiredRefSiblings(t *testing.T) {
	t.Run("wraps a required $ref+sibling property in allOf", func(t *testing.T) {
		root := mustParse(t, `
required:
  - Id
  - Name
properties:
  Id:
    $ref: '#/components/schemas/Resource_Id'
    readOnly: true
  Name:
    $ref: '#/components/schemas/Resource_Name'
    readOnly: true
  Label:
    type: string
`)
		fixRequiredRefSiblings(root)
		out := mustDump(t, root)
		if !strings.Contains(out, "allOf:") {
			t.Errorf("expected allOf wrapping for required $ref+sibling properties:\n%s", out)
		}
		if strings.Count(out, "allOf:") != 2 {
			t.Errorf("expected exactly 2 allOf wraps (Id, Name), got:\n%s", out)
		}
	})

	t.Run("leaves non-required $ref+sibling properties untouched", func(t *testing.T) {
		root := mustParse(t, `
required:
  - Id
properties:
  Id:
    $ref: '#/components/schemas/Resource_Id'
    readOnly: true
  Bios:
    $ref: '#/components/schemas/odata-v4_idRef'
    readOnly: true
`)
		fixRequiredRefSiblings(root)
		out := mustDump(t, root)
		if strings.Count(out, "allOf:") != 1 {
			t.Errorf("expected exactly 1 allOf wrap (only Id is required), got:\n%s", out)
		}
	})

	t.Run("leaves a bare $ref with no siblings untouched", func(t *testing.T) {
		root := mustParse(t, `
required:
  - Actions
properties:
  Actions:
    $ref: '#/components/schemas/Session_v1_7_1_Actions'
`)
		fixRequiredRefSiblings(root)
		out := mustDump(t, root)
		if strings.Contains(out, "allOf:") {
			t.Errorf("a $ref with no sibling keys needs no wrapping:\n%s", out)
		}
	})

	t.Run("recurses into nested schema objects", func(t *testing.T) {
		root := mustParse(t, `
components:
  schemas:
    Widget:
      required:
        - Id
      properties:
        Id:
          $ref: '#/components/schemas/Resource_Id'
          readOnly: true
`)
		fixRequiredRefSiblings(root)
		out := mustDump(t, root)
		if !strings.Contains(out, "allOf:") {
			t.Errorf("expected the nested Widget schema's Id to be wrapped:\n%s", out)
		}
	})
}

func TestRun(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "openapi.yaml")
	allowlistPath := filepath.Join(dir, "implemented-operations.yaml")
	bundleDir := filepath.Join(dir, "bundle")
	schemasDir := filepath.Join(dir, "schemas")
	if err := os.Mkdir(bundleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(schemasDir, 0o755); err != nil {
		t.Fatal(err)
	}

	spec := `
paths:
  /redfish/v1/SessionService/Sessions:
    post:
      requestBody:
        content:
          application/json:
            schema:
              $ref: http://redfish.dmtf.org/schemas/v1/Session.v1_7_1.yaml#/components/schemas/Session_v1_7_1_Session
      responses:
        default:
          content:
            application/json:
              schema:
                $ref: '#/components/schemas/RedfishError'
  /redfish/v1/Chassis:
    get:
      responses:
        '200':
          content:
            application/json:
              schema:
                $ref: ./schemas/ChassisCollection.yaml#/components/schemas/ChassisCollection
components:
  schemas:
    RedfishError:
      properties:
        error:
          items:
            $ref: http://redfish.dmtf.org/schemas/v1/Message.v1_2_0.yaml#/components/schemas/Message_v1_2_0_Message
`
	if err := os.WriteFile(specPath, []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
	allowlist := "POST /redfish/v1/SessionService/Sessions\n"
	if err := os.WriteFile(allowlistPath, []byte(allowlist), 0o644); err != nil {
		t.Fatal(err)
	}

	session := `
components:
  schemas:
    Session_v1_7_1_Session:
      required:
        - Id
        - Name
      properties:
        Id:
          $ref: http://redfish.dmtf.org/schemas/v1/Resource.yaml#/components/schemas/Resource_Id
          readOnly: true
        Name:
          $ref: http://redfish.dmtf.org/schemas/v1/Resource.yaml#/components/schemas/Resource_Name
          readOnly: true
        UserName:
          type: string
`
	if err := os.WriteFile(filepath.Join(bundleDir, "Session.v1_7_1.yaml"), []byte(session), 0o644); err != nil {
		t.Fatal(err)
	}
	resource := `
components:
  schemas:
    Resource_Id:
      type: string
      readOnly: true
    Resource_Name:
      type: string
      readOnly: true
`
	if err := os.WriteFile(filepath.Join(bundleDir, "Resource.yaml"), []byte(resource), 0o644); err != nil {
		t.Fatal(err)
	}
	message := `
components:
  schemas:
    Message_v1_2_0_Message:
      properties:
        Message:
          type: string
`
	if err := os.WriteFile(filepath.Join(bundleDir, "Message.v1_2_0.yaml"), []byte(message), 0o644); err != nil {
		t.Fatal(err)
	}

	// Simulate output from an earlier invocation: Session must be refreshed
	// from the bundle, while ChassisCollection must be purged because its
	// operation is no longer allowlisted. ChassisCollection is deliberately
	// absent from the bundle so the run also proves it is not enqueued.
	if err := os.WriteFile(filepath.Join(schemasDir, "Session.v1_7_1.yaml"), []byte("stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(schemasDir, "ChassisCollection.yaml"), []byte("stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(schemasDir, "README.md"), []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := run(specPath, allowlistPath, bundleDir, schemasDir); err != nil {
		t.Fatalf("run: %v", err)
	}

	vendoredSession, err := os.ReadFile(filepath.Join(schemasDir, "Session.v1_7_1.yaml"))
	if err != nil {
		t.Fatalf("Session.v1_7_1.yaml was not vendored: %v", err)
	}
	if !strings.Contains(string(vendoredSession), "./Resource.yaml#/components/schemas/Resource_Id") {
		t.Errorf("vendored Session file should reference Resource.yaml locally:\n%s", vendoredSession)
	}
	if !strings.Contains(string(vendoredSession), "allOf:") {
		t.Errorf("vendored Session file should have Id/Name wrapped in allOf:\n%s", vendoredSession)
	}
	if strings.Contains(string(vendoredSession), "stale") {
		t.Errorf("existing Session file should be refreshed from the bundle:\n%s", vendoredSession)
	}

	if _, err := os.Stat(filepath.Join(schemasDir, "Resource.yaml")); err != nil {
		t.Errorf("Resource.yaml (transitively referenced) should have been vendored: %v", err)
	}

	if _, err := os.Stat(filepath.Join(schemasDir, "ChassisCollection.yaml")); err == nil {
		t.Error("stale ChassisCollection.yaml should be purged: its operation isn't allowlisted")
	}
	if _, err := os.Stat(filepath.Join(schemasDir, "Message.v1_2_0.yaml")); err != nil {
		t.Errorf("Message.v1_2_0.yaml referenced by a shared component should be vendored: %v", err)
	}
	if readme, err := os.ReadFile(filepath.Join(schemasDir, "README.md")); err != nil || string(readme) != "keep me\n" {
		t.Errorf("non-YAML files should be preserved, got %q, err %v", readme, err)
	}

	rewrittenSpec, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rewrittenSpec), "./schemas/Session.v1_7_1.yaml#/components/schemas/Session_v1_7_1_Session") {
		t.Errorf("spec's allowlisted operation should reference the vendored schema locally:\n%s", rewrittenSpec)
	}
	if !strings.Contains(string(rewrittenSpec), "http://redfish.dmtf.org/schemas/v1/ChassisCollection.yaml") {
		t.Errorf("spec's non-allowlisted operation must have its external ref restored:\n%s", rewrittenSpec)
	}
	if !strings.Contains(string(rewrittenSpec), "./schemas/Message.v1_2_0.yaml#/components/schemas/Message_v1_2_0_Message") {
		t.Errorf("shared component's schema ref should be localized:\n%s", rewrittenSpec)
	}

	// Re-running against the now-vendored output must be idempotent: the
	// reachable set is refreshed without duplicate wrapping.
	if err := run(specPath, allowlistPath, bundleDir, schemasDir); err != nil {
		t.Fatalf("second run: %v", err)
	}
	again, err := os.ReadFile(filepath.Join(schemasDir, "Session.v1_7_1.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(vendoredSession) {
		t.Errorf("second run should reproduce identical schema output:\nfirst:\n%s\nsecond:\n%s", vendoredSession, again)
	}
	afterSecondRun, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterSecondRun) != string(rewrittenSpec) {
		t.Errorf("second run should leave the rewritten spec unchanged:\nfirst:\n%s\nsecond:\n%s", rewrittenSpec, afterSecondRun)
	}
}
