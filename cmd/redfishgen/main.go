// Command redfishgen generates reflection-free Go source for a chosen subset of
// Redfish resources from the CSDL schema bundle.
//
// Usage:
//
//	redfishgen -schema schema/csdl -out gen/redfish_gen.go -pkg redfishgen \
//	    ServiceRoot.ServiceRoot ChassisCollection.ChassisCollection
//
// Each positional argument is an unversioned CSDL type reference. Only the named
// resources (and the complex/enum types they transitively require) are emitted.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

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
	flag.Parse()

	targets := flag.Args()
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
