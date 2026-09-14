package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestParseAllowlist(t *testing.T) {
	t.Run("valid entries, comments and blank lines ignored", func(t *testing.T) {
		src := `
# implemented operations
GET /redfish/v1

post /redfish/v1/SessionService/Sessions
DELETE /redfish/v1/SessionService/Sessions/{SessionId}
`
		got, err := parseAllowlist(strings.NewReader(src))
		if err != nil {
			t.Fatalf("parseAllowlist: %v", err)
		}
		want := []operationKey{
			{Method: "GET", Path: "/redfish/v1"},
			{Method: "POST", Path: "/redfish/v1/SessionService/Sessions"},
			{Method: "DELETE", Path: "/redfish/v1/SessionService/Sessions/{SessionId}"},
		}
		if len(got) != len(want) {
			t.Fatalf("got %d entries, want %d: %v", len(got), len(want), got)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("entry %d = %+v, want %+v", i, got[i], want[i])
			}
		}
	})

	t.Run("unknown method is rejected", func(t *testing.T) {
		_, err := parseAllowlist(strings.NewReader("FETCH /redfish/v1\n"))
		if err == nil {
			t.Fatal("expected an error for an unrecognized HTTP method, got nil")
		}
	})

	t.Run("malformed line is rejected", func(t *testing.T) {
		_, err := parseAllowlist(strings.NewReader("/redfish/v1\n"))
		if err == nil {
			t.Fatal("expected an error for a line missing a method, got nil")
		}
	})
}

// decodePaths parses a YAML fragment of the form `paths: {...}` and returns
// the "paths" mapping node, mirroring the shape trimPaths operates on inside
// a full OpenAPI document.
func decodePaths(t *testing.T, src string) *yaml.Node {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	root := doc.Content[0]
	paths := findMappingValue(root, "paths")
	if paths == nil {
		t.Fatalf("fixture has no top-level paths key")
	}
	return paths
}

func nodeToMap(t *testing.T, n *yaml.Node) map[string]any {
	t.Helper()
	var m map[string]any
	if err := n.Decode(&m); err != nil {
		t.Fatalf("decode node: %v", err)
	}
	return m
}

func TestTrimPaths(t *testing.T) {
	src := `
paths:
  /redfish/v1/Systems:
    get:
      summary: list systems
  /redfish/v1/Systems/{ComputerSystemId}:
    parameters:
      - name: ComputerSystemId
        in: path
    get:
      summary: get a system
    patch:
      summary: update a system
    delete:
      summary: delete a system
  /redfish/v1/Chassis:
    get:
      summary: list chassis
`
	paths := decodePaths(t, src)
	allow := map[operationKey]bool{
		{Method: "GET", Path: "/redfish/v1/Systems"}:                      true,
		{Method: "GET", Path: "/redfish/v1/Systems/{ComputerSystemId}"}:   true,
		{Method: "PATCH", Path: "/redfish/v1/Systems/{ComputerSystemId}"}: true,
	}
	matched := map[operationKey]bool{}

	if err := trimPaths(paths, allow, matched); err != nil {
		t.Fatalf("trimPaths: %v", err)
	}

	got := nodeToMap(t, paths)

	if _, ok := got["/redfish/v1/Chassis"]; ok {
		t.Error("/redfish/v1/Chassis has no allowlisted operations and should have been dropped entirely")
	}

	systems, ok := got["/redfish/v1/Systems"].(map[string]any)
	if !ok {
		t.Fatal("/redfish/v1/Systems should still be present")
	}
	if len(systems) != 1 {
		t.Errorf("/redfish/v1/Systems should only keep its allowlisted GET, got keys %v", systems)
	}

	byID, ok := got["/redfish/v1/Systems/{ComputerSystemId}"].(map[string]any)
	if !ok {
		t.Fatal("/redfish/v1/Systems/{ComputerSystemId} should still be present")
	}
	if _, ok := byID["delete"]; ok {
		t.Error("DELETE on /redfish/v1/Systems/{ComputerSystemId} is not allowlisted and should have been dropped")
	}
	if _, ok := byID["get"]; !ok {
		t.Error("GET on /redfish/v1/Systems/{ComputerSystemId} is allowlisted and should have been kept")
	}
	if _, ok := byID["patch"]; !ok {
		t.Error("PATCH on /redfish/v1/Systems/{ComputerSystemId} is allowlisted and should have been kept")
	}
	if _, ok := byID["parameters"]; !ok {
		t.Error("non-method sibling keys (parameters) should survive when at least one method is kept")
	}

	wantMatched := []operationKey{
		{Method: "GET", Path: "/redfish/v1/Systems"},
		{Method: "GET", Path: "/redfish/v1/Systems/{ComputerSystemId}"},
		{Method: "PATCH", Path: "/redfish/v1/Systems/{ComputerSystemId}"},
	}
	for _, k := range wantMatched {
		if !matched[k] {
			t.Errorf("expected %+v to be recorded as matched", k)
		}
	}
}

func TestTrimPaths_dropsPathWhenNoSiblingMethodSurvives(t *testing.T) {
	src := `
paths:
  /redfish/v1/Chassis/{ChassisId}:
    parameters:
      - name: ChassisId
        in: path
    get:
      summary: get a chassis
`
	paths := decodePaths(t, src)
	// Nothing in this path is allowlisted, so even though "parameters" is a
	// non-method key, the whole path entry must be dropped.
	if err := trimPaths(paths, map[operationKey]bool{}, map[operationKey]bool{}); err != nil {
		t.Fatalf("trimPaths: %v", err)
	}
	got := nodeToMap(t, paths)
	if len(got) != 0 {
		t.Errorf("expected all paths to be dropped, got %v", got)
	}
}

// TestRun_AgainstVendoredSpec is the drift guard: it runs the real tool
// against the real vendored spec and the real allowlist. If DMTF renames a
// path on the next schema bundle bump, this fails here -- loudly, in CI --
// instead of silently dropping an implemented operation out of the
// generated server.
func TestRun_AgainstVendoredSpec(t *testing.T) {
	out := filepath.Join(t.TempDir(), "trimmed.openapi.yaml")

	if err := run("../spec/openapi.yaml", "../spec/implemented-operations.yaml", out); err != nil {
		t.Fatalf("run against the vendored spec and allowlist: %v", err)
	}

	allowFile, err := os.Open("../spec/implemented-operations.yaml")
	if err != nil {
		t.Fatalf("open allowlist: %v", err)
	}
	defer allowFile.Close() //nolint:errcheck
	allow, err := parseAllowlist(allowFile)
	if err != nil {
		t.Fatalf("parse allowlist: %v", err)
	}

	trimmed, err := os.Open(out)
	if err != nil {
		t.Fatalf("open trimmed spec: %v", err)
	}
	defer trimmed.Close() //nolint:errcheck
	var doc yaml.Node
	if err := yaml.NewDecoder(trimmed).Decode(&doc); err != nil {
		t.Fatalf("parse trimmed spec: %v", err)
	}
	paths := findMappingValue(doc.Content[0], "paths")
	if paths == nil {
		t.Fatal("trimmed spec has no paths key")
	}

	gotOps := 0
	for i := 1; i < len(paths.Content); i += 2 {
		item := paths.Content[i]
		for j := 0; j+1 < len(item.Content); j += 2 {
			if httpMethods[strings.ToLower(item.Content[j].Value)] {
				gotOps++
			}
		}
	}
	if gotOps != len(allow) {
		t.Errorf("trimmed spec has %d operations, want %d (one per allowlist entry)", gotOps, len(allow))
	}

	var remoteRefs []string
	var walk func(*yaml.Node)
	walk = func(node *yaml.Node) {
		if node == nil {
			return
		}
		if node.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(node.Content); i += 2 {
				key, value := node.Content[i], node.Content[i+1]
				if key.Value == "$ref" && (strings.HasPrefix(value.Value, "http://") || strings.HasPrefix(value.Value, "https://")) {
					remoteRefs = append(remoteRefs, value.Value)
				}
				walk(value)
			}
			return
		}
		for _, child := range node.Content {
			walk(child)
		}
	}
	walk(&doc)
	if len(remoteRefs) > 0 {
		t.Errorf("trimmed spec still requires remote schemas: %v", remoteRefs)
	}
}

func TestUnmatched(t *testing.T) {
	allow := []operationKey{
		{Method: "GET", Path: "/redfish/v1"},
		{Method: "GET", Path: "/redfish/v1/Renamed"},
	}
	matched := map[operationKey]bool{
		{Method: "GET", Path: "/redfish/v1"}: true,
	}
	got := unmatched(allow, matched)
	if len(got) != 1 || got[0].Path != "/redfish/v1/Renamed" {
		t.Errorf("unmatched = %v, want a single entry for /redfish/v1/Renamed", got)
	}
}
