// Command trim-redfish-spec strips every Redfish operation without a real
// KubeVirtBMC implementation out of the OpenAPI spec before it reaches
// openapi-generator. The DMTF spec defines thousands of operations; only a
// handful are implemented (see pkg/redfish/api_service.go), and generating
// stubs for the rest bloats the codegen output and dilutes test coverage
// (kubevirtbmc/kubevirtbmc#264, #269 fixed the runtime cost of this; this
// tool addresses the build-time and coverage cost).
//
// The spec's local components.schemas has a single entry (RedfishError) --
// every other generated model comes from openapi-generator resolving
// external http://redfish.dmtf.org/... $refs it discovers while walking
// paths. So trimming paths down to the implemented set, with no other
// change to the document, is enough: openapi-generator will only resolve
// the external schemas the remaining operations actually reach.
//
// Usage (invoked from hack/redfish/generate.sh, before openapi-generator):
//
//	go run ./hack/redfish/trim-redfish-spec \
//	    -input hack/redfish/spec/openapi.yaml \
//	    -allowlist hack/redfish/spec/implemented-operations.yaml \
//	    -output hack/redfish/spec/.trimmed.openapi.yaml
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// httpMethods are the OpenAPI 3 path item fixed fields that represent an
// operation, as opposed to sibling fields like "parameters" or "summary".
var httpMethods = map[string]bool{
	"get": true, "put": true, "post": true, "delete": true,
	"options": true, "head": true, "patch": true, "trace": true,
}

// operationKey identifies a single Redfish operation by its HTTP method
// (upper case) and exact path template, e.g. {"GET", "/redfish/v1/Systems"}.
type operationKey struct {
	Method string
	Path   string
}

func main() {
	input := flag.String("input", "hack/redfish/spec/openapi.yaml", "OpenAPI spec to trim")
	allowlistPath := flag.String("allowlist", "hack/redfish/spec/implemented-operations.yaml", "file listing implemented \"METHOD /path\" operations, one per line")
	output := flag.String("output", "hack/redfish/spec/.trimmed.openapi.yaml", "file to write the trimmed spec to")
	flag.Parse()

	if err := run(*input, *allowlistPath, *output); err != nil {
		fmt.Fprintf(os.Stderr, "trim-redfish-spec: %v\n", err)
		os.Exit(1)
	}
}

func run(inputPath, allowlistPath, outputPath string) error {
	allowFile, err := os.Open(allowlistPath)
	if err != nil {
		return fmt.Errorf("open allowlist: %w", err)
	}
	defer allowFile.Close() //nolint:errcheck

	allow, err := parseAllowlist(allowFile)
	if err != nil {
		return fmt.Errorf("parse allowlist %s: %w", allowlistPath, err)
	}
	allowSet := make(map[operationKey]bool, len(allow))
	for _, k := range allow {
		allowSet[k] = true
	}

	specFile, err := os.Open(inputPath)
	if err != nil {
		return fmt.Errorf("open spec: %w", err)
	}
	defer specFile.Close() //nolint:errcheck

	var doc yaml.Node
	if err := yaml.NewDecoder(specFile).Decode(&doc); err != nil {
		return fmt.Errorf("parse spec %s: %w", inputPath, err)
	}
	if len(doc.Content) != 1 {
		return fmt.Errorf("spec %s: expected a single YAML document", inputPath)
	}
	root := doc.Content[0]

	paths := findMappingValue(root, "paths")
	if paths == nil {
		return fmt.Errorf("spec %s: no top-level \"paths\" key", inputPath)
	}

	matched := make(map[operationKey]bool, len(allow))
	if err := trimPaths(paths, allowSet, matched); err != nil {
		return fmt.Errorf("trim paths: %w", err)
	}

	if missing := unmatched(allow, matched); len(missing) > 0 {
		var b strings.Builder
		fmt.Fprintf(&b, "%d allowlisted operation(s) not found in %s (renamed or removed upstream?):\n", len(missing), inputPath)
		for _, k := range missing {
			fmt.Fprintf(&b, "  - %s %s\n", k.Method, k.Path)
		}
		return errors.New(b.String())
	}

	outFile, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("create output: %w", err)
	}
	defer outFile.Close() //nolint:errcheck

	enc := yaml.NewEncoder(outFile)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return fmt.Errorf("write %s: %w", outputPath, err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("write %s: %w", outputPath, err)
	}

	fmt.Fprintf(os.Stderr, "trim-redfish-spec: kept %d operation(s) -> %s\n", len(allow), outputPath)
	return nil
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

// findMappingValue returns the value node for key in a YAML mapping node,
// or nil if mapping isn't a mapping or doesn't contain key.
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

// trimPaths keeps only the operations named in allow under the "paths"
// mapping node, removing any path item left with no remaining HTTP-method
// field. Non-method sibling fields (parameters, summary, description, ...)
// are preserved alongside whatever methods survive. Every allowlist entry
// found in the document is recorded in matched.
func trimPaths(paths *yaml.Node, allow map[operationKey]bool, matched map[operationKey]bool) error {
	if paths.Kind != yaml.MappingNode {
		return fmt.Errorf("paths node is a %v, not a mapping", paths.Kind)
	}

	kept := make([]*yaml.Node, 0, len(paths.Content))
	for i := 0; i+1 < len(paths.Content); i += 2 {
		pathKey, pathItem := paths.Content[i], paths.Content[i+1]

		if pathItem.Kind != yaml.MappingNode {
			// Not a plain path item (e.g. a $ref) -- leave untouched rather
			// than risk dropping something we don't understand.
			kept = append(kept, pathKey, pathItem)
			continue
		}

		keptOps := make([]*yaml.Node, 0, len(pathItem.Content))
		hasMethod := false
		for j := 0; j+1 < len(pathItem.Content); j += 2 {
			opKey, opVal := pathItem.Content[j], pathItem.Content[j+1]
			method := strings.ToLower(opKey.Value)
			if !httpMethods[method] {
				keptOps = append(keptOps, opKey, opVal)
				continue
			}
			key := operationKey{Method: strings.ToUpper(method), Path: pathKey.Value}
			if !allow[key] {
				continue
			}
			matched[key] = true
			keptOps = append(keptOps, opKey, opVal)
			hasMethod = true
		}

		if !hasMethod {
			continue
		}
		pathItem.Content = keptOps
		kept = append(kept, pathKey, pathItem)
	}

	paths.Content = kept
	return nil
}

// unmatched returns the entries of allow that trimPaths never found in the
// document, in the order they appear in allow.
func unmatched(allow []operationKey, matched map[operationKey]bool) []operationKey {
	var missing []operationKey
	for _, k := range allow {
		if !matched[k] {
			missing = append(missing, k)
		}
	}
	sort.Slice(missing, func(i, j int) bool {
		if missing[i].Path != missing[j].Path {
			return missing[i].Path < missing[j].Path
		}
		return missing[i].Method < missing[j].Method
	})
	return missing
}
