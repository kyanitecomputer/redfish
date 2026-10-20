package gen

import (
	"testing"

	"src.kyanite.computer/redfish/internal/csdl"
)

func buildModel(t *testing.T, targets ...string) *Model {
	t.Helper()
	b, err := csdl.LoadDir("../../schema/csdl")
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	m, err := Build(b, "gen", targets)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return m
}

func findAction(t *testing.T, m *Model, resource, action string) Action {
	t.Helper()
	for _, r := range m.Resources {
		if r.GoName != resource {
			continue
		}
		for _, a := range r.Actions {
			if a.GoName == action {
				return a
			}
		}
	}
	t.Fatalf("action %s.%s not found", resource, action)
	return Action{}
}

func TestActionArrayParams(t *testing.T) {
	m := buildModel(t, "UpdateService.UpdateService", "SoftwareInventory.SoftwareInventory")

	tests := []struct {
		resource, action, param string
		wantKind                Kind
		wantType                string
	}{
		{"UpdateService", "SimpleUpdate", "Targets", KindStringSlice, "[]string"},
		{"UpdateService", "SimpleUpdate", "ExcludeTargets", KindStringSlice, "[]string"},
		{"UpdateService", "Activate", "Targets", KindNavSlice, "[]odata.Link"},
		{"SoftwareInventory", "Activate", "Targets", KindNavSlice, "[]odata.Link"},
	}
	for _, tt := range tests {
		t.Run(tt.resource+"."+tt.action+"."+tt.param, func(t *testing.T) {
			a := findAction(t, m, tt.resource, tt.action)
			for _, p := range a.Params {
				if p.JSONName == tt.param {
					if p.Kind != tt.wantKind || p.GoType != tt.wantType {
						t.Errorf("param = {Kind: %v, GoType: %q}, want {%v, %q}", p.Kind, p.GoType, tt.wantKind, tt.wantType)
					}
					return
				}
			}
			t.Errorf("param %s missing; have %v", tt.param, a.Params)
		})
	}
}
