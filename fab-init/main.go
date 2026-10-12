// fab-init performs Step 2 and Step 3 of the Fabrica storage-annotations
// workflow (see fabrica/examples/12-storage-annotations/README.md) against an
// already-initialized Fabrica service:
//
//	Step 2: fabrica add resource <Name>
//	Step 3: merge the hand-written *Spec struct (preserving +fabrica:
//	        annotations and comments) into apis/<group>/<version>/<name>_types.go
//
// Usage:
//
//	fab-init <structs> <service-root>
//
//	<structs>       a .go file, or a directory of .go files, each declaring one
//	                <Name>Spec struct that names the resource.
//	<service-root>  the root of a fabrica-based service (contains apis.yaml).
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"unicode"
)

// resourceSpec describes one resource to add and merge.
type resourceSpec struct {
	name       string // Fabrica resource name, e.g. User
	partsFile  string // path to the file holding the hand-written struct
	structName string // struct to merge, e.g. UserSpec
	typesFile  string // generated file (relative to service root) to merge into
}

func main() {
	fabrica := flag.String("fabrica", "fabrica", "path to the fabrica binary")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: %s [-fabrica path] <structs> <service-root>\n", filepath.Base(os.Args[0]))
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 2 {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(*fabrica, flag.Arg(0), flag.Arg(1)); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// run adds and merges every resource found under structsPath into serviceRoot.
func run(fabrica, structsPath, serviceRoot string) error {
	group, version, err := readGroupVersion(serviceRoot)
	if err != nil {
		return err
	}
	resources, err := resourcesFrom(structsPath, group, version)
	if err != nil {
		return err
	}
	for _, r := range resources {
		// Step 2: scaffold the resource.
		if err := runCmd(serviceRoot, fabrica, "add", "resource", r.name); err != nil {
			return err
		}
	}
	for _, r := range resources {
		// Step 3: merge the annotated struct into the scaffolded types file.
		if err := mergeSpec(serviceRoot, r); err != nil {
			return err
		}
	}
	return goimports(serviceRoot, resources)
}

// readGroupVersion extracts the API group and storage version from apis.yaml.
func readGroupVersion(serviceRoot string) (group, version string, err error) {
	data, err := os.ReadFile(filepath.Join(serviceRoot, "apis.yaml"))
	if err != nil {
		return "", "", fmt.Errorf("reading apis.yaml: %w", err)
	}
	for _, l := range strings.Split(string(data), "\n") {
		t := strings.TrimSpace(l)
		switch {
		case group == "" && strings.HasPrefix(t, "- name:"):
			group = strings.TrimSpace(strings.TrimPrefix(t, "- name:"))
		case group == "" && strings.HasPrefix(t, "name:"):
			group = strings.TrimSpace(strings.TrimPrefix(t, "name:"))
		case strings.HasPrefix(t, "storageVersion:"):
			version = strings.TrimSpace(strings.TrimPrefix(t, "storageVersion:"))
		}
	}
	if group == "" {
		return "", "", errors.New("could not determine API group from apis.yaml")
	}
	if version == "" {
		version = "v1"
	}
	return group, version, nil
}

// resourcesFrom builds a resourceSpec for each .go file under structsPath,
// deriving the resource name from the file's single <Name>Spec struct.
func resourcesFrom(structsPath, group, version string) ([]resourceSpec, error) {
	info, err := os.Stat(structsPath)
	if err != nil {
		return nil, err
	}
	var files []string
	if info.IsDir() {
		entries, err := os.ReadDir(structsPath)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			fn := e.Name()
			if e.IsDir() || !strings.HasSuffix(fn, ".go") || strings.HasSuffix(fn, "_test.go") {
				continue
			}
			files = append(files, filepath.Join(structsPath, fn))
		}
	} else {
		files = []string{structsPath}
	}

	var resources []resourceSpec
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		specName, err := findSpecStruct(string(data))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		name := titleFirst(strings.TrimSuffix(specName, "Spec"))
		resources = append(resources, resourceSpec{
			name:       name,
			partsFile:  f,
			structName: specName,
			typesFile:  filepath.Join("apis", group, version, strings.ToLower(name)+"_types.go"),
		})
	}
	if len(resources) == 0 {
		return nil, fmt.Errorf("no .go struct files found at %s", structsPath)
	}
	return resources, nil
}

// mergeSpec replaces the scaffolded Spec struct with the hand-written one,
// appends any extra structs, and adds resource annotations when present.
func mergeSpec(serviceRoot string, r resourceSpec) error {
	partsData, err := os.ReadFile(r.partsFile)
	if err != nil {
		return err
	}
	parts := string(partsData)
	block, err := extractStructBlock(parts, r.structName)
	if err != nil {
		return fmt.Errorf("%s: %w", r.partsFile, err)
	}
	target := filepath.Join(serviceRoot, r.typesFile)
	orig, err := os.ReadFile(target)
	if err != nil {
		return err
	}
	merged, err := replaceStructBlock(string(orig), r.structName, block)
	if err != nil {
		return fmt.Errorf("%s: %w", target, err)
	}
	merged, added, err := appendExtraStructs(merged, parts, r.structName)
	if err != nil {
		return fmt.Errorf("%s: %w", r.partsFile, err)
	}
	annotated := false
	if strings.Contains(block, "+fabrica:") {
		merged, annotated, err = addResourceAnnotations(merged, r.name)
		if err != nil {
			return fmt.Errorf("%s: %w", target, err)
		}
	}
	if err := os.WriteFile(target, []byte(merged), 0o644); err != nil {
		return err
	}
	fmt.Printf("+ merged %s into %s", r.structName, r.typesFile)
	if len(added) > 0 {
		fmt.Printf(" (+ structs: %s)", strings.Join(added, ", "))
	}
	if annotated {
		fmt.Printf(" (+ resource annotations on %s)", r.name)
	}
	fmt.Println()
	return nil
}

// addResourceAnnotations inserts +fabrica:resource and +fabrica:storage=dedicated
// directives above the resource struct declaration. It is a no-op (returns
// added=false) when a +fabrica:resource directive is already present.
func addResourceAnnotations(src, resourceName string) (string, bool, error) {
	lines := strings.Split(src, "\n")
	re := regexp.MustCompile(`^\s*type\s+` + regexp.QuoteMeta(resourceName) + `\s+struct\s*{`)
	typeLine := -1
	for i, l := range lines {
		if re.MatchString(l) {
			typeLine = i
			break
		}
	}
	if typeLine == -1 {
		return "", false, fmt.Errorf("resource struct %q not found", resourceName)
	}
	commentStart := typeLine
	for commentStart > 0 && strings.HasPrefix(strings.TrimSpace(lines[commentStart-1]), "//") {
		commentStart--
	}
	for i := commentStart; i < typeLine; i++ {
		if strings.Contains(lines[i], "+fabrica:resource") {
			return src, false, nil
		}
	}
	var insert []string
	if commentStart < typeLine {
		insert = []string{"//", "// +fabrica:resource", "// +fabrica:storage=dedicated"}
	} else {
		insert = []string{"// +fabrica:resource", "// +fabrica:storage=dedicated"}
	}
	out := make([]string, 0, len(lines)+len(insert))
	out = append(out, lines[:typeLine]...)
	out = append(out, insert...)
	out = append(out, lines[typeLine:]...)
	return strings.Join(out, "\n"), true, nil
}

// appendExtraStructs copies every struct in parts except specStruct into dst.
// A struct already present in dst is skipped when its name ends in "Spec" and
// rejected otherwise. Returns the updated source and the names appended.
func appendExtraStructs(dst, parts, specStruct string) (string, []string, error) {
	existing := structNameSet(dst)
	var blocks, added []string
	for _, name := range structNames(parts) {
		if name == specStruct {
			continue
		}
		if existing[name] {
			if strings.HasSuffix(name, "Spec") {
				continue
			}
			return "", nil, fmt.Errorf("generated code already defines struct %q", name)
		}
		block, err := extractStructBlock(parts, name)
		if err != nil {
			return "", nil, err
		}
		blocks = append(blocks, block)
		added = append(added, name)
	}
	if len(blocks) == 0 {
		return dst, nil, nil
	}
	return strings.TrimRight(dst, "\n") + "\n\n" + strings.Join(blocks, "\n\n") + "\n", added, nil
}

// --- struct block extraction / replacement ----------------------------------

// extractStructBlock returns the leading comment/directive lines plus the full
// `type <structName> struct { ... }` declaration, verbatim.
func extractStructBlock(src, structName string) (string, error) {
	lines := strings.Split(src, "\n")
	start, end, ok := structBlockRange(lines, structName)
	if !ok {
		return "", fmt.Errorf("struct %q not found", structName)
	}
	return strings.Join(lines[start:end+1], "\n"), nil
}

// replaceStructBlock swaps the existing struct block for replacement.
func replaceStructBlock(src, structName, replacement string) (string, error) {
	lines := strings.Split(src, "\n")
	start, end, ok := structBlockRange(lines, structName)
	if !ok {
		return "", fmt.Errorf("struct %q not found", structName)
	}
	out := make([]string, 0, len(lines))
	out = append(out, lines[:start]...)
	out = append(out, strings.Split(replacement, "\n")...)
	out = append(out, lines[end+1:]...)
	return strings.Join(out, "\n"), nil
}

var structDeclRe = regexp.MustCompile(`(?m)^\s*type\s+(\w+)\s+struct\s*{`)

// structNames returns struct type names declared in src, in source order.
func structNames(src string) []string {
	var names []string
	for _, m := range structDeclRe.FindAllStringSubmatch(src, -1) {
		names = append(names, m[1])
	}
	return names
}

// structNameSet returns the set of struct type names declared in src.
func structNameSet(src string) map[string]bool {
	set := map[string]bool{}
	for _, n := range structNames(src) {
		set[n] = true
	}
	return set
}

// findSpecStruct returns the single struct name ending in "Spec" in src.
func findSpecStruct(src string) (string, error) {
	var found []string
	for _, n := range structNames(src) {
		if strings.HasSuffix(n, "Spec") && n != "Spec" {
			found = append(found, n)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return "", errors.New("no *Spec struct found")
	default:
		return "", fmt.Errorf("multiple *Spec structs found (%s)", strings.Join(found, ", "))
	}
}

// structBlockRange returns the [start,end] line indexes of the struct block,
// including any contiguous leading // comment lines.
func structBlockRange(lines []string, structName string) (start, end int, ok bool) {
	re := regexp.MustCompile(`^\s*type\s+` + regexp.QuoteMeta(structName) + `\s+struct\s*{`)
	typeLine := -1
	for i, l := range lines {
		if re.MatchString(l) {
			typeLine = i
			break
		}
	}
	if typeLine == -1 {
		return 0, 0, false
	}
	start = typeLine
	for start > 0 && strings.HasPrefix(strings.TrimSpace(lines[start-1]), "//") {
		start--
	}
	endLine, found := structEndLine(lines, typeLine)
	if !found {
		return 0, 0, false
	}
	return start, endLine, true
}

// structEndLine finds the line closing the struct opened at startLine.
func structEndLine(lines []string, startLine int) (int, bool) {
	depth := 0
	opened := false
	for i := startLine; i < len(lines); i++ {
		depth += braceDelta(lines[i])
		if strings.Contains(lines[i], "{") {
			opened = true
		}
		if opened && depth == 0 {
			return i, true
		}
	}
	return 0, false
}

// braceDelta counts { minus } on a line, ignoring braces inside strings,
// raw strings (backticks/struct tags), and // comments.
func braceDelta(line string) int {
	delta := 0
	inRaw := false
	inStr := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case inRaw:
			if c == '`' {
				inRaw = false
			}
		case inStr:
			if c == '"' && line[i-1] != '\\' {
				inStr = false
			}
		case c == '`':
			inRaw = true
		case c == '"':
			inStr = true
		case c == '/' && i+1 < len(line) && line[i+1] == '/':
			return delta
		case c == '{':
			delta++
		case c == '}':
			delta--
		}
	}
	return delta
}

// --- helpers ----------------------------------------------------------------

// runCmd runs name+args in dir, streaming output, and echoes the command.
func runCmd(dir, name string, args ...string) error {
	fmt.Printf("+ %s %s  (in %s)\n", name, strings.Join(args, " "), dir)
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// goimports runs goimports -w on every merged types file so the injected
// structs get their imports resolved.
func goimports(serviceRoot string, resources []resourceSpec) error {
	bin, err := ensureGoimports()
	if err != nil {
		return err
	}
	for _, r := range resources {
		if err := runCmd(serviceRoot, bin, "-w", r.typesFile); err != nil {
			return fmt.Errorf("goimports %s: %w", r.typesFile, err)
		}
	}
	return nil
}

// ensureGoimports returns a path to goimports, installing it if necessary.
func ensureGoimports() (string, error) {
	if bin, err := exec.LookPath("goimports"); err == nil {
		return bin, nil
	}
	if bin := gopathGoimports(); bin != "" {
		return bin, nil
	}
	if err := runCmd(".", "go", "install", "golang.org/x/tools/cmd/goimports@latest"); err != nil {
		return "", fmt.Errorf("installing goimports: %w", err)
	}
	if bin, err := exec.LookPath("goimports"); err == nil {
		return bin, nil
	}
	if bin := gopathGoimports(); bin != "" {
		return bin, nil
	}
	return "", errors.New("goimports not found on PATH or in GOPATH/bin after install")
}

// gopathGoimports returns the goimports path under GOPATH/bin if it exists.
func gopathGoimports() string {
	out, err := exec.Command("go", "env", "GOPATH").Output()
	if err != nil {
		return ""
	}
	gopath := strings.TrimSpace(string(out))
	if gopath == "" {
		return ""
	}
	bin := filepath.Join(gopath, "bin", "goimports")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if _, err := os.Stat(bin); err == nil {
		return bin
	}
	return ""
}

// titleFirst upper-cases the first rune (user -> User).
func titleFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}
