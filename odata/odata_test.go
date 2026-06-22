package odata

import (
	"bytes"
	"encoding/json"
	"testing"
)

// valid decodes b to confirm the builder emits syntactically valid JSON.
func valid(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("invalid JSON %q: %v", b, err)
	}
	return m
}

func TestAppendStringEscaping(t *testing.T) {
	cases := map[string]string{
		`plain`:          `"plain"`,
		"with\"quote":    `"with\"quote"`,
		"back\\slash":    `"back\\slash"`,
		"tab\tnl\n":      `"tab\tnl\n"`,
		"ctrl\x01":       `"ctrl\u0001"`,
		"unicode-\u00e9": "\"unicode-\u00e9\"", // multibyte UTF-8 passes through
	}
	for in, want := range cases {
		if got := string(AppendString(nil, in)); got != want {
			t.Errorf("AppendString(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestBuilderOrderingAndTypes(t *testing.T) {
	b := NewBuilder()
	b.String("@odata.id", "/redfish/v1")
	b.OptString("skip", "")
	b.Int("Count", 3)
	b.Bool("Flag", true)
	b.Null("Reading")
	b.Ref("Systems", "/redfish/v1/Systems")
	got := b.Bytes()

	m := valid(t, got)
	if m["@odata.id"] != "/redfish/v1" {
		t.Errorf("@odata.id = %v", m["@odata.id"])
	}
	if _, ok := m["skip"]; ok {
		t.Error("empty OptString should be omitted")
	}
	if m["Count"].(float64) != 3 {
		t.Errorf("Count = %v", m["Count"])
	}
	if m["Reading"] != nil {
		t.Errorf("Reading should be JSON null, got %v", m["Reading"])
	}
	ref := m["Systems"].(map[string]any)
	if ref["@odata.id"] != "/redfish/v1/Systems" {
		t.Errorf("ref = %v", ref)
	}
	// Integers must be JSON numbers, never strings.
	if bytes.Contains(got, []byte(`"3"`)) {
		t.Errorf("integer encoded as string: %s", got)
	}
}

func TestCollectionShape(t *testing.T) {
	c := Collection{
		ODataType:    "#ChassisCollection.ChassisCollection",
		ODataID:      "/redfish/v1/Chassis",
		ODataContext: Context("ChassisCollection.ChassisCollection"),
		Name:         "Chassis Collection",
		Members:      []string{"/redfish/v1/Chassis/1", "/redfish/v1/Chassis/2"},
	}
	m := valid(t, c.Bytes())
	if m["Members@odata.count"].(float64) != 2 {
		t.Errorf("count = %v", m["Members@odata.count"])
	}
	members := m["Members"].([]any)
	if len(members) != 2 {
		t.Fatalf("members len = %d", len(members))
	}
	first := members[0].(map[string]any)
	if first["@odata.id"] != "/redfish/v1/Chassis/1" {
		t.Errorf("first member = %v", first)
	}
	if m["@odata.context"] != "/redfish/v1/$metadata#ChassisCollection.ChassisCollection" {
		t.Errorf("context = %v", m["@odata.context"])
	}
}

func TestErrorBody(t *testing.T) {
	e := Error{
		Code:    MsgResourceNotFound,
		Message: "The resource was not found.",
		ExtendedInfo: []ExtendedInfo{
			{MessageID: MsgResourceNotFound, Message: "Not found", Severity: "Critical"},
		},
	}
	m := valid(t, e.Bytes())
	inner := m["error"].(map[string]any)
	if inner["code"] != MsgResourceNotFound {
		t.Errorf("code = %v", inner["code"])
	}
	info := inner["@Message.ExtendedInfo"].([]any)
	if len(info) != 1 {
		t.Fatalf("extended info len = %d", len(info))
	}
}
