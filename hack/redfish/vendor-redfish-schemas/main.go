// Command vendor-redfish-schemas inlines the external DMTF schema files
// reachable from the implemented Redfish operations into a local, checked-in
// copy, so openapi-generator resolves them from disk instead of fetching
// http://redfish.dmtf.org/... live on every `make generate-redfish-api` run.
//
// That live fetch used to be the reason hack/redfish/relax-identity-required-fields
// existed: DMTF's schemas declare Id/Name via a $ref with a `readOnly: true`
// sibling key (e.g. `Id: {$ref: .../Resource.yaml#/..., readOnly: true}`),
// and OpenAPI 3.0.x requires a compliant parser to ignore sibling keys next
// to $ref -- so readOnly never reaches openapi-generator's go-server
// template, which has (since v7.24.0) correctly stopped requiring readOnly
// fields on request bodies, but only for fields it can see are readOnly.
// Now that we own these files locally, we fix that at the source instead:
// wrap the $ref in allOf so the sibling survives, scoped to properties that
// are actually in the schema's "required" list (wrapping unconditionally
// changes openapi-generator's codegen shape -- pointer-ness,
// Assert*Required calls -- for unrelated, non-required fields that happen
// to also be readOnly). That retired relax-identity-required-fields
// entirely, which used to guard against a real 422 ("field 'Id' is
// required") on session-create and computer-system-patch bodies -- no
// Redfish client ever sends Id/Name, since both are server-assigned.
//
// This is a one-time/occasional tool, not part of the regular
// hack/redfish/generate.sh pipeline: run it by hand after `make
// download-redfish-schema`, when adding a new implemented-operations.yaml
// entry or refreshing the DMTF schema bundle version, and commit whatever it
// writes under hack/redfish/spec/schemas/.
//
// Usage:
//
//	go run ./hack/redfish/vendor-redfish-schemas \
//	    -spec hack/redfish/spec/openapi.yaml \
//	    -allowlist hack/redfish/spec/implemented-operations.yaml \
//	    -bundle <extracted DSP8010 bundle>/openapi \
//	    -schemas-dir hack/redfish/spec/schemas
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// httpMethods are the OpenAPI 3 path item fixed fields that represent an
// operation, as opposed to sibling fields like "parameters" or "summary".
var httpMethods = map[string]bool{
	"get": true, "put": true, "post": true, "delete": true,
	"options": true, "head": true, "patch": true, "trace": true,
}

type operationKey struct {
	Method string
	Path   string
}

func main() {
	spec := flag.String("spec", "hack/redfish/spec/openapi.yaml", "OpenAPI spec to rewrite in place")
	allowlistPath := flag.String("allowlist", "hack/redfish/spec/implemented-operations.yaml", "file listing implemented \"METHOD /path\" operations, one per line")
	bundleDir := flag.String("bundle", "", "path to the openapi/ directory of an extracted DSP8010 schema bundle (see: make download-redfish-schema)")
	schemasDir := flag.String("schemas-dir", "hack/redfish/spec/schemas", "directory to vendor referenced schema files into")
	flag.Parse()

	if *bundleDir == "" {
		fmt.Fprintln(os.Stderr, "vendor-redfish-schemas: -bundle is required")
		os.Exit(1)
	}

	if err := run(*spec, *allowlistPath, *bundleDir, *schemasDir); err != nil {
		fmt.Fprintf(os.Stderr, "vendor-redfish-schemas: %v\n", err)
		os.Exit(1)
	}
}

func run(specPath, allowlistPath, bundleDir, schemasDir string) error {
	allow, err := parseAllowlistFile(allowlistPath)
	if err != nil {
		return fmt.Errorf("parse allowlist %s: %w", allowlistPath, err)
	}
	allowSet := make(map[operationKey]bool, len(allow))
	for _, k := range allow {
		allowSet[k] = true
	}

	rawSpec, err := os.ReadFile(specPath)
	if err != nil {
		return fmt.Errorf("read spec %s: %w", specPath, err)
	}
	_, root, err := decodeYAMLBytes(rawSpec)
	if err != nil {
		return fmt.Errorf("parse spec %s: %w", specPath, err)
	}
	paths := findMappingValue(root, "paths")
	if paths == nil {
		return fmt.Errorf("spec %s: no top-level \"paths\" key", specPath)
	}

	if err := os.MkdirAll(filepath.Dir(schemasDir), 0o755); err != nil {
		return fmt.Errorf("create parent of %s: %w", schemasDir, err)
	}
	stagingDir, err := os.MkdirTemp(filepath.Dir(schemasDir), ".redfish-schemas-")
	if err != nil {
		return fmt.Errorf("create staging directory for %s: %w", schemasDir, err)
	}
	defer os.RemoveAll(stagingDir) //nolint:errcheck

	// The top-level spec lives one directory above schemasDir (e.g.
	// hack/redfish/spec/openapi.yaml -> hack/redfish/spec/schemas/), so its
	// $refs need a "schemas/"-style prefix that refs between vendored files
	// (already siblings) don't.
	relSchemasDir, err := filepath.Rel(filepath.Dir(specPath), schemasDir)
	if err != nil {
		return fmt.Errorf("relative path from %s to %s: %w", specPath, schemasDir, err)
	}
	specDirPrefix := filepath.ToSlash(relSchemasDir) + "/"

	v := &vendorer{bundleDir: bundleDir, schemasDir: stagingDir, vendored: map[string]bool{}}

	// Collect ref edits without mutating the tree: openapi.yaml is a
	// previously-committed, human-reviewed 170k-line file, and re-encoding
	// the whole document through yaml.v3 reformats every long description
	// string in it (different line-wrap width, quote style) -- a ~130k-line
	// diff for what's semantically an 18-line change. Applying just the
	// exact ref substitutions as line-scoped text edits keeps the rest of
	// the file byte-identical.
	var edits []refEdit
	rewritten := 0
	for i := 0; i+1 < len(paths.Content); i += 2 {
		pathKey, pathItem := paths.Content[i], paths.Content[i+1]
		if pathItem.Kind != yaml.MappingNode {
			continue
		}
		for j := 0; j+1 < len(pathItem.Content); j += 2 {
			opKey, opVal := pathItem.Content[j], pathItem.Content[j+1]
			method := strings.ToLower(opKey.Value)
			if !httpMethods[method] {
				continue
			}
			key := operationKey{Method: strings.ToUpper(method), Path: pathKey.Value}
			if allowSet[key] {
				edits = append(edits, collectRefEdits(opVal, specDirPrefix, true, v.enqueue)...)
				rewritten++
			} else {
				// If an operation was removed from the allowlist, restore any
				// schema refs a previous run localized. The full source spec
				// remains valid after its now-unreachable schemas are purged.
				edits = append(edits, collectRefEdits(opVal, specDirPrefix, false, v.enqueue)...)
			}
		}
	}

	// Top-level components survive path trimming and are therefore reachable
	// from every generated spec. Vendor their external refs too; RedfishError,
	// for example, references Message.v1_2_0.yaml.
	if components := findMappingValue(root, "components"); components != nil {
		edits = append(edits, collectRefEdits(components, specDirPrefix, true, v.enqueue)...)
	}

	patchedSpec, err := applyLineEdits(rawSpec, edits)
	if err != nil {
		return fmt.Errorf("patch %s: %w", specPath, err)
	}
	if err := v.drain(); err != nil {
		return err
	}
	if err := replaceVendoredSchemas(stagingDir, schemasDir); err != nil {
		return err
	}
	if err := os.WriteFile(specPath, patchedSpec, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", specPath, err)
	}

	fmt.Fprintf(os.Stderr, "vendor-redfish-schemas: rewrote %d operation(s), vendored %d schema file(s) -> %s\n", rewritten, len(v.vendored), schemasDir)
	return nil
}

// vendorer fetches and inlines external schema files referenced (directly
// or transitively) by the allowlisted operations, applying the
// required+readOnly $ref-sibling fix to each as it's vendored.
type vendorer struct {
	bundleDir  string
	schemasDir string
	vendored   map[string]bool // filename -> already vendored (or queued)
	queue      []string
}

func (v *vendorer) enqueue(filename string) {
	if v.vendored[filename] {
		return
	}
	v.vendored[filename] = true
	v.queue = append(v.queue, filename)
}

func (v *vendorer) drain() error {
	for len(v.queue) > 0 {
		filename := v.queue[0]
		v.queue = v.queue[1:]

		doc, root, err := decodeYAMLFile(filepath.Join(v.bundleDir, filename))
		if err != nil {
			return fmt.Errorf("read bundle schema %s: %w", filename, err)
		}
		fixRequiredRefSiblings(root)
		rewriteExternalRefs(root, "", v.enqueue)

		outPath := filepath.Join(v.schemasDir, filename)
		if err := encodeYAMLFile(outPath, doc); err != nil {
			return fmt.Errorf("write %s: %w", outPath, err)
		}
	}
	return nil
}

// replaceVendoredSchemas makes schemasDir's YAML files exactly match the
// reachable set built in stagingDir. Non-YAML files such as README.md are
// preserved.
func replaceVendoredSchemas(stagingDir, schemasDir string) error {
	if err := os.MkdirAll(schemasDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", schemasDir, err)
	}
	existing, err := filepath.Glob(filepath.Join(schemasDir, "*.yaml"))
	if err != nil {
		return fmt.Errorf("list existing schemas in %s: %w", schemasDir, err)
	}
	for _, filename := range existing {
		if err := os.Remove(filename); err != nil {
			return fmt.Errorf("remove stale schema %s: %w", filename, err)
		}
	}
	staged, err := filepath.Glob(filepath.Join(stagingDir, "*.yaml"))
	if err != nil {
		return fmt.Errorf("list staged schemas in %s: %w", stagingDir, err)
	}
	for _, filename := range staged {
		destination := filepath.Join(schemasDir, filepath.Base(filename))
		if err := os.Rename(filename, destination); err != nil {
			return fmt.Errorf("install schema %s: %w", destination, err)
		}
	}
	return nil
}

// rewriteExternalRefs walks n for every {$ref: "http(s)://...", ...} node
// and rewrites the $ref value in place to a local path relative to the
// document n lives in ("<dirPrefix><file>.yaml#<fragment>"), calling
// enqueue with the referenced filename. Purely local refs
// ("#/components/schemas/...") are left untouched. Used for freshly-vendored
// files, which get fully re-encoded anyway so in-place mutation is fine; see
// collectExternalRefEdits for the previously-committed top-level spec, where
// a full re-encode isn't acceptable.
func rewriteExternalRefs(n *yaml.Node, dirPrefix string, enqueue func(filename string)) {
	if n == nil {
		return
	}
	switch n.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, c := range n.Content {
			rewriteExternalRefs(c, dirPrefix, enqueue)
		}
		return
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, val := n.Content[i], n.Content[i+1]
			if k.Value == "$ref" && val.Kind == yaml.ScalarNode {
				if filename, fragment, ok := splitExternalRef(val.Value); ok {
					val.Value = "./" + dirPrefix + filename + fragment
					enqueue(filename)
				}
				continue
			}
			rewriteExternalRefs(val, dirPrefix, enqueue)
		}
	}
}

// refEdit is a single $ref rewrite located at its original line number (1
// indexed, as reported by yaml.v3), to be applied as a text substitution
// rather than a tree mutation + re-encode.
type refEdit struct {
	line int
	old  string
	new  string
}

// collectRefEdits records the top-level spec edits needed to localize refs
// reachable from generated output, or restore localized refs for operations
// removed from the allowlist. n must still carry line numbers from rawSpec.
func collectRefEdits(n *yaml.Node, dirPrefix string, localize bool, enqueue func(filename string)) []refEdit {
	var edits []refEdit
	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		if n == nil {
			return
		}
		switch n.Kind {
		case yaml.DocumentNode, yaml.SequenceNode:
			for _, c := range n.Content {
				walk(c)
			}
		case yaml.MappingNode:
			for i := 0; i+1 < len(n.Content); i += 2 {
				k, val := n.Content[i], n.Content[i+1]
				if k.Value == "$ref" && val.Kind == yaml.ScalarNode {
					filename, fragment, external := splitExternalRef(val.Value)
					if external && localize {
						edits = append(edits, refEdit{
							line: val.Line,
							old:  val.Value,
							new:  "./" + dirPrefix + filename + fragment,
						})
						enqueue(filename)
					} else if filename, fragment, managed := splitManagedLocalRef(val.Value, dirPrefix); managed {
						if localize {
							enqueue(filename)
						} else {
							edits = append(edits, refEdit{
								line: val.Line,
								old:  val.Value,
								new:  dmtfSchemaBaseURL + filename + fragment,
							})
						}
					}
					continue
				}
				walk(val)
			}
		}
	}
	walk(n)
	return edits
}

const dmtfSchemaBaseURL = "http://redfish.dmtf.org/schemas/v1/"

func splitManagedLocalRef(ref, dirPrefix string) (filename, fragment string, ok bool) {
	prefix := "./" + dirPrefix
	if !strings.HasPrefix(ref, prefix) {
		return "", "", false
	}
	base, frag := ref[len(prefix):], ""
	if i := strings.Index(base, "#"); i >= 0 {
		base, frag = base[:i], base[i:]
	}
	if base == "" || filepath.Base(base) != base {
		return "", "", false
	}
	return base, frag, true
}

// applyLineEdits applies each edit as a single literal substring
// replacement on its recorded line, leaving every other line of raw
// byte-for-byte untouched. Errors if a line's content has drifted from what
// collectExternalRefEdits saw (e.g. this func was handed the wrong file).
func applyLineEdits(raw []byte, edits []refEdit) ([]byte, error) {
	if len(edits) == 0 {
		return raw, nil
	}
	lines := strings.Split(string(raw), "\n")
	for _, e := range edits {
		idx := e.line - 1
		if idx < 0 || idx >= len(lines) {
			return nil, fmt.Errorf("line %d out of range (file has %d lines)", e.line, len(lines))
		}
		if !strings.Contains(lines[idx], e.old) {
			return nil, fmt.Errorf("line %d: expected to find %q, got %q", e.line, e.old, lines[idx])
		}
		lines[idx] = strings.Replace(lines[idx], e.old, e.new, 1)
	}
	return []byte(strings.Join(lines, "\n")), nil
}

// splitExternalRef splits an absolute http(s) $ref like
// "http://redfish.dmtf.org/schemas/v1/Resource.yaml#/components/schemas/X"
// into ("Resource.yaml", "#/components/schemas/X"). ok is false for a
// purely local ref (no scheme), which callers should leave untouched.
func splitExternalRef(ref string) (filename, fragment string, ok bool) {
	if !strings.HasPrefix(ref, "http://") && !strings.HasPrefix(ref, "https://") {
		return "", "", false
	}
	base, frag := ref, ""
	if i := strings.Index(ref, "#"); i >= 0 {
		base, frag = ref[:i], ref[i:]
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", "", false
	}
	return path.Base(u.Path), frag, true
}

// fixRequiredRefSiblings walks a document looking for schema objects (a
// mapping with both "properties" and "required" keys) and, for every
// property NAME in that schema's "required" list whose value is a bare
// {$ref, <siblings>} node, wraps it in allOf so sibling keys (readOnly,
// nullable, ...) survive OpenAPI 3.0.x $ref-sibling stripping. Properties
// with $ref+siblings that are NOT required are left untouched, since
// wrapping changes openapi-generator's codegen shape for the field
// (composed-schema instead of a plain alias: pointer-ness,
// Assert*Required/Constraints calls, etc) and non-required fields don't
// need the fix.
func fixRequiredRefSiblings(n *yaml.Node) {
	if n == nil {
		return
	}
	switch n.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, c := range n.Content {
			fixRequiredRefSiblings(c)
		}
		return
	case yaml.MappingNode:
		props := findMappingValue(n, "properties")
		required := findMappingValue(n, "required")
		if props != nil && required != nil && props.Kind == yaml.MappingNode {
			reqSet := make(map[string]bool, len(required.Content))
			for _, r := range required.Content {
				reqSet[r.Value] = true
			}
			for i := 0; i+1 < len(props.Content); i += 2 {
				name, val := props.Content[i].Value, props.Content[i+1]
				if reqSet[name] && val.Kind == yaml.MappingNode {
					wrapRefWithSiblingsInAllOf(val)
				}
			}
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			fixRequiredRefSiblings(n.Content[i+1])
		}
	}
}

// wrapRefWithSiblingsInAllOf rewrites {$ref: X, sibling: ...} in place into
// {allOf: [{$ref: X}], sibling: ...}. A $ref with no sibling keys is left
// alone (nothing to preserve).
func wrapRefWithSiblingsInAllOf(n *yaml.Node) {
	refIdx := -1
	var refVal *yaml.Node
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == "$ref" {
			refIdx, refVal = i, n.Content[i+1]
		}
	}
	if refIdx < 0 || len(n.Content)/2 == 1 {
		return
	}

	refMap := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
		{Kind: yaml.ScalarNode, Tag: "!!str", Value: "$ref"},
		{Kind: yaml.ScalarNode, Tag: "!!str", Value: refVal.Value},
	}}
	allOf := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{refMap}}

	newContent := []*yaml.Node{
		{Kind: yaml.ScalarNode, Tag: "!!str", Value: "allOf"},
		allOf,
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if i == refIdx {
			continue
		}
		newContent = append(newContent, n.Content[i], n.Content[i+1])
	}
	n.Content = newContent
}

func findMappingValue(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

func parseAllowlistFile(filePath string) ([]operationKey, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck
	return parseAllowlist(f)
}

// parseAllowlist reads "METHOD /path" lines, one operation per line. Blank
// lines and lines starting with "#" are ignored.
func parseAllowlist(r io.Reader) ([]operationKey, error) {
	var keys []operationKey
	scanner := bufio.NewScanner(r)
	for lineNo := 1; scanner.Scan(); lineNo++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, fmt.Errorf("line %d: expected \"METHOD /path\", got %q", lineNo, line)
		}
		method := strings.ToUpper(fields[0])
		if !httpMethods[strings.ToLower(method)] {
			return nil, fmt.Errorf("line %d: %q is not a recognized HTTP method", lineNo, fields[0])
		}
		keys = append(keys, operationKey{Method: method, Path: fields[1]})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return keys, nil
}

func decodeYAMLFile(filePath string) (doc *yaml.Node, root *yaml.Node, err error) {
	raw, err := os.ReadFile(filePath)
	if err != nil {
		return nil, nil, err
	}
	return decodeYAMLBytes(raw)
}

func decodeYAMLBytes(raw []byte) (doc *yaml.Node, root *yaml.Node, err error) {
	doc = &yaml.Node{}
	if err := yaml.Unmarshal(raw, doc); err != nil {
		return nil, nil, err
	}
	if len(doc.Content) != 1 {
		return nil, nil, fmt.Errorf("expected a single YAML document")
	}
	return doc, doc.Content[0], nil
}

func encodeYAMLFile(filePath string, doc *yaml.Node) error {
	f, err := os.Create(filePath)
	if err != nil {
		return err
	}
	defer f.Close() //nolint:errcheck

	enc := yaml.NewEncoder(f)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return err
	}
	return enc.Close()
}
