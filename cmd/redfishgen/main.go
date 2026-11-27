// Command redfishgen generates reflection-free Go source for a chosen subset of
// Redfish resources from the CSDL schema bundle.
//
// Usage:
//
//	redfishgen -schema schema/csdl -out gen/redfish_gen.go -pkg redfishgen \
//	    ServiceRoot.ServiceRoot ChassisCollection.ChassisCollection
//	redfishgen -schema schema/csdl -out gen/redfish_gen.go -pkg gen -targets gen/targets.txt
//
// Each positional argument, and each line of the -targets file, is an
// unversioned CSDL type reference. Only the named resources (and the
// complex/enum types they transitively require) are emitted.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"src.kyanite.computer/redfish/internal/csdl"
	"src.kyanite.computer/redfish/internal/gen"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "redfishgen:", err)
		os.Exit(1)
	}
}

func run() error {
	schemaDir := flag.String("schema", "schema/csdl", "directory containing CSDL XML files")
	out := flag.String("out", "", "output Go file (default: stdout)")
	pkg := flag.String("pkg", "redfishgen", "package name for generated code")
	targetsFile := flag.String("targets", "", "file listing target resources, one per line; # starts a comment")
	flag.Parse()

	targets := flag.Args()
	if *targetsFile != "" {
		listed, err := readTargets(*targetsFile)
		if err != nil {
			return err
		}
		targets = append(listed, targets...)
	}
	if len(targets) == 0 {
		return fmt.Errorf("no target resources given")
	}

	bundle, err := csdl.LoadDir(*schemaDir)
	if err != nil {
		return err
	}

	model, err := gen.Build(bundle, *pkg, targets)
	if err != nil {
		return err
	}

	src, err := gen.Render(model)
	if err != nil {
		return err
	}

	if *out == "" {
		_, err = os.Stdout.Write(src)
		return err
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		return err
	}
	return os.WriteFile(*out, src, 0o644)
}

// readTargets reads a targets file: one type reference per line, with blank
// lines and everything after a # left out.
func readTargets(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var targets []string
	for line := range strings.Lines(string(data)) {
		line, _, _ = strings.Cut(line, "#")
		if t := strings.TrimSpace(line); t != "" {
			targets = append(targets, t)
		}
	}
	return targets, nil
}
