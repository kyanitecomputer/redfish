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

// TestWriteOnlyAndCreate checks that a write-only property such as
// ManagerAccount.Password is writable, and which properties a creating POST
// may carry.
func TestWriteOnlyAndCreate(t *testing.T) {
	m := buildModel(t, "ManagerAccount.ManagerAccount", "Session.Session", "ServiceRoot.ServiceRoot")
	names := func(fields []Field) map[string]bool {
		out := map[string]bool{}
		for _, f := range fields {
			out[f.JSONName] = true
		}
		return out
	}
	res := map[string]*Resource{}
	for _, r := range m.Resources {
		res[r.GoName] = r
	}
	account, session, root := res["ManagerAccount"], res["Session"], res["ServiceRoot"]
	if account == nil || session == nil || root == nil {
		t.Fatalf("resources = %v", res)
	}
	if w := names(account.WritableFields()); !w["Password"] || !w["UserName"] || w["Id"] {
		t.Errorf("ManagerAccount writable = %v, want Password and UserName, not Id", w)
	}
	if c := names(account.CreateFields()); !account.Creatable() || !c["Password"] || !c["RoleId"] {
		t.Errorf("ManagerAccount create = %v", c)
	}
	// Session.UserName and Password are read-only once created, but required
	// on create.
	if c := names(session.CreateFields()); !session.Creatable() || !c["UserName"] || !c["Password"] || c["Id"] {
		t.Errorf("Session create = %v, want UserName and Password only", c)
	}
	for _, f := range account.Fields {
		if (f.JSONName == "Password") != f.WriteOnly {
			t.Errorf("ManagerAccount.%s WriteOnly = %v", f.JSONName, f.WriteOnly)
		}
	}
	for _, f := range session.Fields {
		if (f.JSONName == "Password" || f.JSONName == "Token") != f.WriteOnly {
			t.Errorf("Session.%s WriteOnly = %v", f.JSONName, f.WriteOnly)
		}
	}
	if root.Creatable() {
		t.Error("ServiceRoot is creatable")
	}
}
