package update

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"src.kyanite.computer/redfish/gen"
	"src.kyanite.computer/redfish/task"
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
	tasks := task.New()
	tasks.Register(mux)
	New(b, tasks).Register(mux)
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

// fakeInstaller records install requests and blocks each until released.
type fakeInstaller struct {
	*fakeBackend
	release chan error
	got     chan installCall
}

type installCall struct {
	img  Image
	req  Request
	data string // the pushed image, read from img.Body
}

func newFakeInstaller() *fakeInstaller {
	return &fakeInstaller{fakeBackend: newFake(), release: make(chan error), got: make(chan installCall, 1)}
}

func (f *fakeInstaller) Install(_ context.Context, img Image, req Request, progress func(int)) error {
	call := installCall{img: img, req: req}
	if img.Body != nil {
		data, err := io.ReadAll(img.Body)
		if err != nil {
			return err
		}
		call.data, call.img.Body = string(data), nil
	}
	f.got <- call
	progress(50)
	return <-f.release
}

func TestSimpleUpdateNotAdvertisedWithoutInstaller(t *testing.T) {
	srv := newTestServer(t, newFake())
	if _, m := do(t, "GET", srv.URL+ServiceURI, ""); m["Actions"] != nil {
		t.Errorf("Actions = %v, want none", m["Actions"])
	}
	if code, _ := do(t, "POST", srv.URL+SimpleUpdateURI, `{"ImageURI":"http://x/i"}`); code != http.StatusMethodNotAllowed && code != http.StatusNotFound {
		t.Errorf("POST SimpleUpdate: status %d, want 404 or 405", code)
	}
}

func TestSimpleUpdate(t *testing.T) {
	b := newFakeInstaller()
	srv := newTestServer(t, b)

	_, m := do(t, "GET", srv.URL+ServiceURI, "")
	su, _ := m["Actions"].(map[string]any)["#UpdateService.SimpleUpdate"].(map[string]any)
	if su["target"] != SimpleUpdateURI {
		t.Fatalf("SimpleUpdate target = %v", su)
	}

	body := `{"ImageURI":"fw.example/bmc.bin","TransferProtocol":"HTTPS","Username":"u","Password":"p",` +
		`"Targets":["/redfish/v1/UpdateService/FirmwareInventory/bmc"],"Stage":true}`
	resp, err := http.Post(srv.URL+SimpleUpdateURI, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	monitor := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusAccepted || !strings.HasPrefix(monitor, task.MonitorURI+"/") {
		t.Fatalf("POST SimpleUpdate: status %d, Location %q", resp.StatusCode, monitor)
	}

	call := <-b.got
	wantImg := Image{URI: "https://fw.example/bmc.bin", Username: "u", Password: "p"}
	if call.img != wantImg {
		t.Errorf("image = %+v, want %+v", call.img, wantImg)
	}
	if len(call.req.Targets) != 1 || call.req.Targets[0] != "/redfish/v1/UpdateService/FirmwareInventory/bmc" || !call.req.Stage || call.req.ForceUpdate {
		t.Errorf("request = %+v", call.req)
	}

	if code, _ := do(t, "POST", srv.URL+SimpleUpdateURI, `{"ImageURI":"http://x/i"}`); code != http.StatusServiceUnavailable {
		t.Errorf("concurrent update: status %d, want 503", code)
	}

	b.release <- nil
	deadline := time.Now().Add(5 * time.Second)
	for {
		code, _ := do(t, "GET", srv.URL+monitor, "")
		if code == http.StatusNoContent {
			break
		}
		if code != http.StatusAccepted || time.Now().After(deadline) {
			t.Fatalf("task monitor: status %d", code)
		}
		time.Sleep(5 * time.Millisecond)
	}

	if code, _ := do(t, "POST", srv.URL+SimpleUpdateURI, `{"ImageURI":"http://x/i"}`); code != http.StatusAccepted {
		t.Errorf("update after completion: status %d, want 202", code)
	}
	<-b.got
	b.release <- nil
}

func TestSimpleUpdateRejected(t *testing.T) {
	tests := []struct {
		name       string
		disable    bool
		body       string
		wantStatus int
	}{
		{"missing ImageURI", false, `{}`, http.StatusBadRequest},
		{"LocalImage", false, `{"ImageURI":"bmc.bin","LocalImage":true}`, http.StatusBadRequest},
		{"malformed", false, `{"ImageURI":`, http.StatusBadRequest},
		{"disabled", true, `{"ImageURI":"http://x/i"}`, http.StatusServiceUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTestServer(t, newFakeInstaller())
			if tt.disable {
				do(t, "PATCH", srv.URL+ServiceURI, `{"ServiceEnabled":false}`)
			}
			if code, _ := do(t, "POST", srv.URL+SimpleUpdateURI, tt.body); code != tt.wantStatus {
				t.Errorf("status %d, want %d", code, tt.wantStatus)
			}
		})
	}
}
