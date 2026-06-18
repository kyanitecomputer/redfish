package csdl

import (
	"testing"
)

const schemaDir = "../../schema/csdl"

func loadBundle(t *testing.T) *Bundle {
	t.Helper()
	b, err := LoadDir(schemaDir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	return b
}

func TestLatestNamespace(t *testing.T) {
	b := loadBundle(t)
	ns, ok := b.LatestNamespace("ServiceRoot")
	if !ok {
		t.Fatal("no versioned ServiceRoot namespace found")
	}
	// The bundle ships ServiceRoot v1.21.0; ensure we pick a v1_x and not v1_0_0.
	if ns == "ServiceRoot.v1_0_0" {
		t.Fatalf("LatestNamespace returned the oldest version: %s", ns)
	}
	if _, ok := b.Schema(ns); !ok {
		t.Fatalf("latest namespace %s not indexed", ns)
	}
}

func TestResolveServiceRoot(t *testing.T) {
	b := loadBundle(t)
	ns, et, ok := b.LatestEntity("ServiceRoot.ServiceRoot")
	if !ok {
		t.Fatal("could not resolve ServiceRoot.ServiceRoot")
	}
	if !isVersioned(ns) {
		t.Fatalf("expected versioned namespace, got %q", ns)
	}
	if et.Name != "ServiceRoot" {
		t.Fatalf("got entity %q, want ServiceRoot", et.Name)
	}
	// RedfishVersion is defined on the v1_0_0 leaf and inherited upward; the
	// resolved leaf type itself should chain to a base.
	if et.BaseType == "" {
		t.Fatal("expected ServiceRoot leaf to declare a BaseType")
	}
}

func TestResolveCollectionAndMembers(t *testing.T) {
	b := loadBundle(t)
	_, et, ok := b.ResolveEntity("ChassisCollection.ChassisCollection")
	if !ok {
		t.Fatal("could not resolve ChassisCollection")
	}
	var members *NavigationProperty
	for i := range et.NavProps {
		if et.NavProps[i].Name == "Members" {
			members = &et.NavProps[i]
		}
	}
	if members == nil {
		t.Fatal("ChassisCollection has no Members navigation property")
	}
	inner, isColl := StripCollection(members.Type)
	if !isColl {
		t.Fatalf("Members type %q is not a collection", members.Type)
	}
	if inner != "Chassis.Chassis" {
		t.Fatalf("Members inner type = %q, want Chassis.Chassis", inner)
	}
}

func TestResolveBaseResourceProperties(t *testing.T) {
	b := loadBundle(t)
	// Resource.v1_0_0.Resource defines Id, Name, Description used by every
	// resource; confirm we can reach it and its properties.
	_, et, ok := b.ResolveEntity("Resource.v1_0_0.Resource")
	if !ok {
		t.Fatal("could not resolve Resource.v1_0_0.Resource")
	}
	want := map[string]bool{"Id": false, "Name": false, "Description": false}
	for _, p := range et.Properties {
		if _, tracked := want[p.Name]; tracked {
			want[p.Name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("Resource is missing property %q", name)
		}
	}
}

func TestServiceRootUrisAnnotation(t *testing.T) {
	b := loadBundle(t)
	_, et, _ := b.ResolveEntity("ServiceRoot.ServiceRoot")
	// Walk to the abstract base that carries Redfish.Uris.
	uris := findUris(b, et)
	if len(uris) == 0 {
		t.Fatal("ServiceRoot has no Redfish.Uris annotation in its chain")
	}
	if uris[0] != "/redfish/v1" {
		t.Fatalf("first ServiceRoot URI = %q, want /redfish/v1", uris[0])
	}
}

func TestBoundActions(t *testing.T) {
	b := loadBundle(t)
	acts := b.BoundActions("ComputerSystem")
	if len(acts) == 0 {
		t.Fatal("ComputerSystem has no bound actions")
	}
	byName := map[string]Action{}
	for _, a := range acts {
		byName[a.Name] = a
	}
	reset, ok := byName["Reset"]
	if !ok {
		t.Fatal("ComputerSystem.Reset action not found")
	}
	if !reset.IsBound {
		t.Error("Reset is not marked IsBound")
	}
	// The binding parameter is first; ResetType is the real input parameter.
	var hasResetType bool
	for _, p := range reset.Parameters[1:] {
		if p.Name == "ResetType" {
			hasResetType = true
		}
	}
	if !hasResetType {
		t.Errorf("Reset is missing a ResetType parameter: %+v", reset.Parameters)
	}
	// Result must be sorted and de-duplicated.
	for i := 1; i < len(acts); i++ {
		if acts[i-1].Name >= acts[i].Name {
			t.Errorf("actions not sorted/unique: %q then %q", acts[i-1].Name, acts[i].Name)
		}
	}
}

// findUris walks the BaseType chain looking for a Redfish.Uris annotation.
func findUris(b *Bundle, et *EntityType) []string {
	for et != nil {
		if u := Annotations(et.Annotations).Strings("Redfish.Uris"); len(u) > 0 {
			return u
		}
		if et.BaseType == "" {
			return nil
		}
		_, base, ok := b.ResolveEntity(et.BaseType)
		if !ok {
			return nil
		}
		et = base
	}
	return nil
}
