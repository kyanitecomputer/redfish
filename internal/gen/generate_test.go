package gen

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"
)

func TestRenderURIlessResource(t *testing.T) {
	src, err := Render(buildModel(t, "ActionInfo.ActionInfo"))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, want := range []string{
		"GetActionInfo(ctx context.Context, p server.Params) (*ActionInfo, error)",
		"func RegisterActionInfo(mux *http.ServeMux, p ActionInfoProvider, uris ...string) {",
		"for _, uri := range uris {\n\t\tserver.Handle(mux, \"GET\", uri, h)\n\t}",
	} {
		if !strings.Contains(string(src), want) {
			t.Errorf("generated source lacks %q", want)
		}
	}
}

// TestRenderTypeChecks type-checks the source generated for single targets,
// each of which needs a different part of the shared helpers.
func TestRenderTypeChecks(t *testing.T) {
	if testing.Short() {
		t.Skip("type-checks the runtime packages from source")
	}
	fset := token.NewFileSet()
	imp := importer.ForCompiler(fset, "source", nil)
	for _, target := range []string{
		"Session.Session",                     // create body only
		"Chassis.Chassis",                     // PATCH and actions
		"ChassisCollection.ChassisCollection", // collection only
		"ActionInfo.ActionInfo",               // no URIs
	} {
		t.Run(target, func(t *testing.T) {
			src, err := Render(buildModel(t, target))
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			f, err := parser.ParseFile(fset, target+".go", src, 0)
			if err != nil {
				t.Fatal(err)
			}
			conf := types.Config{Importer: imp}
			if _, err := conf.Check("gen", fset, []*ast.File{f}, nil); err != nil {
				t.Error(err)
			}
		})
	}
}
