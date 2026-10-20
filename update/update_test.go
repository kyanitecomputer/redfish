package update

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"src.kyanite.computer/redfish/gen"
)

type fakeBackend struct {
	inv Inventory
}

func (f *fakeBackend) Inventory(context.Context) (*Inventory, error) { return &f.inv, nil }

func newFake() *fakeBackend {
	return &fakeBackend{inv: Inventory{
		Firmware: []*gen.SoftwareInventory{{Id: "bmc", Name: "BMC Firmware", Version: "1.0.0"}},
		Software: []*gen.SoftwareInventory{{Id: "os", Name: "Host OS", Version: "6.1"}},
	}}
}

func newTestServer(t *testing.T, b Backend) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	New(b).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func do(t *testing.T, method, url, body string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	return resp.StatusCode, m
}

func TestUpdateService(t *testing.T) {
	srv := newTestServer(t, newFake())
	code, m := do(t, "GET", srv.URL+ServiceURI, "")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if m["@odata.id"] != ServiceURI || m["ServiceEnabled"] != true {
		t.Errorf("body = %v", m)
	}
	if fw, _ := m["FirmwareInventory"].(map[string]any); fw["@odata.id"] != FirmwareInventoryURI {
		t.Errorf("FirmwareInventory = %v", m["FirmwareInventory"])
	}
}

func TestPatchUpdateService(t *testing.T) {
	srv := newTestServer(t, newFake())
	code, m := do(t, "PATCH", srv.URL+ServiceURI, `{"ServiceEnabled":false}`)
	if code != http.StatusOK || m["ServiceEnabled"] != false {
		t.Fatalf("PATCH ServiceEnabled: status = %d, body = %v", code, m)
	}
	code, _ = do(t, "PATCH", srv.URL+ServiceURI, `{"VerifyRemoteServerCertificate":true}`)
	if code != http.StatusBadRequest {
		t.Errorf("PATCH read-only property: status = %d, want 400", code)
	}
}

func TestInventoryCollections(t *testing.T) {
	srv := newTestServer(t, newFake())
	tests := []struct {
		uri        string
		wantStatus int
		wantMember string
	}{
		{FirmwareInventoryURI, http.StatusOK, FirmwareInventoryURI + "/bmc"},
		{SoftwareInventoryURI, http.StatusOK, SoftwareInventoryURI + "/os"},
		{ServiceURI + "/LocalImageStore", http.StatusNotFound, ""},
	}
	for _, tt := range tests {
		t.Run(tt.uri, func(t *testing.T) {
			code, m := do(t, "GET", srv.URL+tt.uri, "")
			if code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", code, tt.wantStatus)
			}
			if tt.wantStatus != http.StatusOK {
				return
			}
			if m["@odata.id"] != tt.uri {
				t.Errorf("@odata.id = %v, want %s", m["@odata.id"], tt.uri)
			}
			members, _ := m["Members"].([]any)
			if len(members) != 1 || members[0].(map[string]any)["@odata.id"] != tt.wantMember {
				t.Errorf("Members = %v, want [%s]", members, tt.wantMember)
			}
		})
	}
}

func TestInventoryMembers(t *testing.T) {
	b := newFake()
	srv := newTestServer(t, b)
	tests := []struct {
		uri        string
		wantStatus int
		wantName   string
	}{
		{FirmwareInventoryURI + "/bmc", http.StatusOK, "BMC Firmware"},
		{SoftwareInventoryURI + "/os", http.StatusOK, "Host OS"},
		{SoftwareInventoryURI + "/bmc", http.StatusNotFound, ""},
		{FirmwareInventoryURI + "/missing", http.StatusNotFound, ""},
	}
	for _, tt := range tests {
		t.Run(tt.uri, func(t *testing.T) {
			code, m := do(t, "GET", srv.URL+tt.uri, "")
			if code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", code, tt.wantStatus)
			}
			if tt.wantStatus == http.StatusOK && (m["@odata.id"] != tt.uri || m["Name"] != tt.wantName) {
				t.Errorf("body = %v", m)
			}
		})
	}
	if b.inv.Firmware[0].ODataID != "" {
		t.Errorf("backend inventory was mutated: ODataID = %q", b.inv.Firmware[0].ODataID)
	}
}
