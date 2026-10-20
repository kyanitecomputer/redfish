package gen

import (
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
