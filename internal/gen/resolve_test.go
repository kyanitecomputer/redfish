package gen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// TestBaseTypeCycle builds types whose BaseType chains loop.
func TestBaseTypeCycle(t *testing.T) {
	const doc = `<?xml version="1.0" encoding="UTF-8"?>
<edmx:Edmx xmlns:edmx="http://docs.oasis-open.org/odata/ns/edmx" Version="4.0">
  <edmx:DataServices>
    <Schema xmlns="http://docs.oasis-open.org/odata/ns/edm" Namespace="Loop.v1_0_0">
      <EntityType Name="Loop" BaseType="Loop.v1_0_0.Other">
        <Property Name="Inner" Type="Loop.v1_0_0.A"/>
      </EntityType>
      <EntityType Name="Other" BaseType="Loop.v1_0_0.Loop"/>
      <ComplexType Name="A" BaseType="Loop.v1_0_0.B"/>
      <ComplexType Name="B" BaseType="Loop.v1_0_0.A"/>
    </Schema>
  </edmx:DataServices>
</edmx:Edmx>`
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Loop_v1.xml"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := csdl.LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := Build(b, "gen", []string{"Loop.Loop"})
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Build loops on a BaseType cycle")
	}
}

func TestGoName(t *testing.T) {
	for _, tt := range []struct{ ns, name, want string }{
		{"ServiceRoot", "Links", "ServiceRootLinks"},
		{"Resource", "Status", "ResourceStatus"},
		{"Task", "TaskState", "TaskState"},
		{"Message", "Message", "Message"},
		{"Chassis", "ChassisType", "ChassisType"},
		{"Manager", "ManagerType", "ManagerType"},
		{"ManagerAccount", "AccountTypes", "ManagerAccountAccountTypes"},
	} {
		if got := goName(tt.ns, tt.name); got != tt.want {
			t.Errorf("goName(%q, %q) = %q, want %q", tt.ns, tt.name, got, tt.want)
		}
	}
}

// TestEnumSlice checks that an array of an enumeration keeps its type.
func TestEnumSlice(t *testing.T) {
	m := buildModel(t, "ManagerAccount.ManagerAccount")
	for _, f := range m.Resources[0].Fields {
		if f.JSONName == "AccountTypes" {
			if f.Kind != KindEnumSlice || f.GoType != "[]ManagerAccountAccountTypes" {
				t.Errorf("AccountTypes = {Kind: %v, GoType: %q}", f.Kind, f.GoType)
			}
			return
		}
	}
	t.Error("no AccountTypes")
}

// TestNameClash checks that two types generated under one Go name fail the
// build: Loop.Links and LoopLinks.LoopLinks are both LoopLinks.
func TestNameClash(t *testing.T) {
	const doc = `<?xml version="1.0" encoding="UTF-8"?>
<edmx:Edmx xmlns:edmx="http://docs.oasis-open.org/odata/ns/edmx" Version="4.0">
  <edmx:DataServices>
    <Schema xmlns="http://docs.oasis-open.org/odata/ns/edm" Namespace="Loop.v1_0_0">
      <EntityType Name="Loop">
        <Property Name="A" Type="Loop.v1_0_0.Links"/>
        <Property Name="B" Type="LoopLinks.v1_0_0.LoopLinks"/>
      </EntityType>
      <ComplexType Name="Links"/>
    </Schema>
    <Schema xmlns="http://docs.oasis-open.org/odata/ns/edm" Namespace="LoopLinks.v1_0_0">
      <ComplexType Name="LoopLinks"/>
    </Schema>
  </edmx:DataServices>
</edmx:Edmx>`
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Loop_v1.xml"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := csdl.LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Build(b, "gen", []string{"Loop.Loop"}); err == nil || !strings.Contains(err.Error(), "LoopLinks") {
		t.Errorf("Build = %v, want a clash of LoopLinks", err)
	}
}

// TestNameKeepsPrefix builds OutboundConnection, whose namespace defines
// both RetryPolicyType and OutboundConnectionRetryPolicyType.
func TestNameKeepsPrefix(t *testing.T) {
	m := buildModel(t, "OutboundConnection.OutboundConnection")
	names := map[string]bool{}
	for _, c := range m.Complexes {
		names[c.GoName] = true
	}
	for _, e := range m.Enums {
		names[e.GoName] = true
	}
	for _, want := range []string{"OutboundConnectionRetryPolicyType", "OutboundConnectionOutboundConnectionRetryPolicyType"} {
		if !names[want] {
			t.Errorf("no type %s", want)
		}
	}
}
