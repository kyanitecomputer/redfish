package main

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// newServer serves the example; requests without credentials log in as
// admin with HTTP Basic.
func newServer(t *testing.T) *httptest.Server {
	t.Helper()
	h, err := newHandler(newBackend())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" && r.Header.Get("X-Auth-Token") == "" {
			r.SetBasicAuth("admin", "admin")
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestAuthentication(t *testing.T) {
	h, err := newHandler(newBackend())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	for path, want := range map[string]int{
		"/redfish/v1/":            http.StatusOK,
		"/redfish/v1/$metadata":   http.StatusOK,
		"/redfish/v1/Chassis":     http.StatusUnauthorized,
		"/redfish/v1/Systems/1":   http.StatusUnauthorized,
		"/redfish/v1/TaskService": http.StatusUnauthorized,
	} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("GET %s without credentials: status %d, want %d", path, resp.StatusCode, want)
		}
	}
	// Log in and use the session.
	resp, err := http.Post(srv.URL+"/redfish/v1/SessionService/Sessions", "application/json",
		strings.NewReader(`{"UserName":"admin","Password":"admin"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	token := resp.Header.Get("X-Auth-Token")
	if resp.StatusCode != http.StatusCreated || token == "" {
		t.Fatalf("login: status %d", resp.StatusCode)
	}
	req, _ := http.NewRequest("GET", srv.URL+"/redfish/v1/Chassis", nil)
	req.Header.Set("X-Auth-Token", token)
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusOK {
		t.Errorf("GET with the session: %v, %v", resp, err)
	} else {
		resp.Body.Close()
	}
}

// getJSON fetches path and decodes the JSON body, asserting status 200 and the
// standard Redfish headers.
func getJSON(t *testing.T, base, path string) map[string]any {
	t.Helper()
	resp, err := http.Get(base + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d", path, resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("GET %s: Content-Type = %q", path, ct)
	}
	if v := resp.Header.Get("OData-Version"); v != "4.0" {
		t.Errorf("GET %s: OData-Version = %q", path, v)
	}
	if resp.Header.Get("ETag") == "" {
		t.Errorf("GET %s: missing ETag", path)
	}
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatalf("GET %s: decode: %v", path, err)
	}
	return m
}

func TestServiceRoot(t *testing.T) {
	srv := newServer(t)
	for _, path := range []string{"/redfish/v1", "/redfish/v1/"} {
		m := getJSON(t, srv.URL, path)
		if m["@odata.id"] != "/redfish/v1" {
			t.Errorf("%s: @odata.id = %v", path, m["@odata.id"])
		}
		if m["@odata.type"] != "#ServiceRoot.v1_21_0.ServiceRoot" {
			t.Errorf("%s: @odata.type = %v", path, m["@odata.type"])
		}
		if m["@odata.context"] != "/redfish/v1/$metadata#ServiceRoot.ServiceRoot" {
			t.Errorf("%s: @odata.context = %v", path, m["@odata.context"])
		}
		if m["RedfishVersion"] != "1.21.0" {
			t.Errorf("%s: RedfishVersion = %v", path, m["RedfishVersion"])
		}
		systems, ok := m["Systems"].(map[string]any)
		if !ok || systems["@odata.id"] != "/redfish/v1/Systems" {
			t.Errorf("%s: Systems link = %v", path, m["Systems"])
		}
	}
}

func TestChassisCollection(t *testing.T) {
	srv := newServer(t)
	m := getJSON(t, srv.URL, "/redfish/v1/Chassis")
	if m["Members@odata.count"].(float64) != 1 {
		t.Errorf("count = %v", m["Members@odata.count"])
	}
	members := m["Members"].([]any)
	first := members[0].(map[string]any)
	if first["@odata.id"] != "/redfish/v1/Chassis/1" {
		t.Errorf("member = %v", first)
	}
}

func TestChassisMember(t *testing.T) {
	srv := newServer(t)
	m := getJSON(t, srv.URL, "/redfish/v1/Chassis/1")
	if m["@odata.id"] != "/redfish/v1/Chassis/1" {
		t.Errorf("@odata.id = %v", m["@odata.id"])
	}
	if m["Id"] != "1" || m["Name"] != "Main Chassis" {
		t.Errorf("Id/Name = %v/%v", m["Id"], m["Name"])
	}
	if m["ChassisType"] != "RackMount" {
		t.Errorf("ChassisType = %v", m["ChassisType"])
	}
}

func TestMetadata(t *testing.T) {
	srv := newServer(t)
	resp, err := http.Get(srv.URL + "/redfish/v1/$metadata")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/xml; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	var doc struct {
		XMLName    xml.Name `xml:"Edmx"`
		References []struct {
			URI string `xml:"Uri,attr"`
		} `xml:"Reference"`
	}
	body, _ := io.ReadAll(resp.Body)
	if err := xml.Unmarshal(body, &doc); err != nil {
		t.Fatalf("metadata is not valid XML: %v", err)
	}
	if len(doc.References) == 0 {
		t.Fatal("metadata has no references")
	}
	if !bytes.Contains(body, []byte("ServiceRoot_v1.xml")) {
		t.Error("metadata missing ServiceRoot reference")
	}
}

func TestODataServiceDoc(t *testing.T) {
	srv := newServer(t)
	m := getJSON(t, srv.URL, "/redfish/v1/odata")
	if m["@odata.context"] != "/redfish/v1/$metadata" {
		t.Errorf("@odata.context = %v", m["@odata.context"])
	}
	values := m["value"].([]any)
	if len(values) < 2 {
		t.Fatalf("service doc has %d entries", len(values))
	}
}

func TestPatchChassis(t *testing.T) {
	srv := newServer(t)
	// Read current ETag.
	resp, err := http.Get(srv.URL + "/redfish/v1/Chassis/1")
	if err != nil {
		t.Fatal(err)
	}
	etag := resp.Header.Get("ETag")
	resp.Body.Close()
	if etag == "" {
		t.Fatal("no ETag on GET")
	}

	// Sparse PATCH with matching If-Match; only AssetTag should change.
	body := `{"AssetTag":"rack-7","Manufacturer":"ignored-readonly","Unknown":123}`
	req, _ := http.NewRequest(http.MethodPatch, srv.URL+"/redfish/v1/Chassis/1", strings.NewReader(body))
	req.Header.Set("If-Match", etag)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PATCH status = %d, want 200", resp.StatusCode)
	}
	var m map[string]any
	json.NewDecoder(resp.Body).Decode(&m)
	if m["AssetTag"] != "rack-7" {
		t.Errorf("AssetTag = %v, want rack-7", m["AssetTag"])
	}
	if m["Manufacturer"] != "Kyanite" {
		t.Errorf("read-only Manufacturer changed: %v", m["Manufacturer"])
	}
}

func TestPatchIfMatchMismatch(t *testing.T) {
	srv := newServer(t)
	req, _ := http.NewRequest(http.MethodPatch, srv.URL+"/redfish/v1/Chassis/1", strings.NewReader(`{"AssetTag":"x"}`))
	req.Header.Set("If-Match", `"deadbeef"`)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("status = %d, want 412", resp.StatusCode)
	}
}

func TestPatchMalformed(t *testing.T) {
	srv := newServer(t)
	req, _ := http.NewRequest(http.MethodPatch, srv.URL+"/redfish/v1/Chassis/1", strings.NewReader(`{not json`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestPatchNotAllowed(t *testing.T) {
	srv := newServer(t)
	// ServiceRoot is not updatable; PATCH must be rejected (405) by the mux.
	req, _ := http.NewRequest(http.MethodPatch, srv.URL+"/redfish/v1", strings.NewReader(`{}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", resp.StatusCode)
	}
}

func TestComputerSystemAdvertisesResetAction(t *testing.T) {
	srv := newServer(t)
	m := getJSON(t, srv.URL, "/redfish/v1/Systems/1")
	actions, ok := m["Actions"].(map[string]any)
	if !ok {
		t.Fatalf("Actions missing or wrong type: %v", m["Actions"])
	}
	reset, ok := actions["#ComputerSystem.Reset"].(map[string]any)
	if !ok {
		t.Fatalf("#ComputerSystem.Reset missing: %v", actions)
	}
	if reset["target"] != "/redfish/v1/Systems/1/Actions/ComputerSystem.Reset" {
		t.Errorf("Reset target = %v", reset["target"])
	}
}

func TestComputerSystemReset(t *testing.T) {
	srv := newServer(t)
	for _, tt := range []struct {
		uri, body string
		status    int
		power     string // PowerState afterwards
	}{
		{"/redfish/v1/Systems/1", `{"ResetType":"ForceOff"}`, http.StatusNoContent, "Off"},
		{"/redfish/v1/Systems/1", `{}`, http.StatusNoContent, "On"},
		{"/redfish/v1/Systems/1", `{"ResetType":"ForceOff"}`, http.StatusNoContent, "Off"},
		{"/redfish/v1/Systems/1", ``, http.StatusNoContent, "On"},
		{"/redfish/v1/Systems/1", `{"ResetType":"Nmi"}`, http.StatusBadRequest, "On"},
		{"/redfish/v1/Systems/nope", `{"ResetType":"On"}`, http.StatusNotFound, "On"},
	} {
		resp, err := http.Post(srv.URL+tt.uri+"/Actions/ComputerSystem.Reset", "application/json", strings.NewReader(tt.body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != tt.status {
			t.Errorf("POST %s %s: status %d, want %d", tt.uri, tt.body, resp.StatusCode, tt.status)
		}
		if m := getJSON(t, srv.URL, "/redfish/v1/Systems/1"); m["PowerState"] != tt.power {
			t.Errorf("after POST %s %s: PowerState = %v, want %s", tt.uri, tt.body, m["PowerState"], tt.power)
		}
	}
}

// TestConcurrentRequests reads and changes the chassis and the system at
// once, for the race detector.
func TestConcurrentRequests(t *testing.T) {
	srv := newServer(t)
	// One login first, so that the others find the password verified
	// instead of waiting to hash it.
	getJSON(t, srv.URL, "/redfish/v1/Chassis")
	get := func(path string) {
		if resp, err := http.Get(srv.URL + path); err == nil {
			resp.Body.Close()
		}
	}
	var wg sync.WaitGroup
	for i := range 4 {
		wg.Go(func() {
			req, _ := http.NewRequest(http.MethodPatch, srv.URL+"/redfish/v1/Chassis/1", strings.NewReader(`{"AssetTag":"t`+strconv.Itoa(i)+`"}`))
			if resp, err := http.DefaultClient.Do(req); err == nil {
				resp.Body.Close()
			}
		})
		wg.Go(func() {
			if resp, err := http.Post(srv.URL+"/redfish/v1/Systems/1/Actions/ComputerSystem.Reset", "application/json", strings.NewReader(`{}`)); err == nil {
				resp.Body.Close()
			}
		})
		wg.Go(func() { get("/redfish/v1/Chassis/1") })
		wg.Go(func() { get("/redfish/v1/Systems/1") })
	}
	wg.Wait()
}

func TestNotFound(t *testing.T) {
	srv := newServer(t)
	resp, err := http.Get(srv.URL + "/redfish/v1/Chassis/missing")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := m["error"]; !ok {
		t.Errorf("missing error body: %v", m)
	}
}

func TestFirmwareInventory(t *testing.T) {
	srv := newServer(t)
	root := getJSON(t, srv.URL, "/redfish/v1")
	if us, _ := root["UpdateService"].(map[string]any); us["@odata.id"] != "/redfish/v1/UpdateService" {
		t.Fatalf("UpdateService link = %v", root["UpdateService"])
	}
	coll := getJSON(t, srv.URL, "/redfish/v1/UpdateService/FirmwareInventory")
	if coll["Members@odata.count"].(float64) != 2 {
		t.Errorf("firmware count = %v", coll["Members@odata.count"])
	}
	mgr := getJSON(t, srv.URL, "/redfish/v1/Managers/bmc")
	active, _ := mgr["Links"].(map[string]any)["ActiveSoftwareImage"].(map[string]any)
	id, _ := active["@odata.id"].(string)
	if id != "/redfish/v1/UpdateService/FirmwareInventory/bmc" {
		t.Fatalf("ActiveSoftwareImage = %v", active)
	}
	fw := getJSON(t, srv.URL, id)
	if fw["Version"] != "1.0.0" || fw["Version"] != mgr["FirmwareVersion"] {
		t.Errorf("BMC firmware version = %v, Manager.FirmwareVersion = %v", fw["Version"], mgr["FirmwareVersion"])
	}
}

func TestPushUpdate(t *testing.T) {
	srv := newServer(t)
	us := getJSON(t, srv.URL, "/redfish/v1/UpdateService")
	pushURI, _ := us["MultipartHttpPushUri"].(string)
	if pushURI == "" {
		t.Fatalf("MultipartHttpPushUri missing: %v", us)
	}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	params, _ := mw.CreateFormField("UpdateParameters")
	params.Write([]byte(`{"Targets":["/redfish/v1/UpdateService/FirmwareInventory/bmc"]}`))
	file, _ := mw.CreateFormFile("UpdateFile", "bmc.bin")
	file.Write([]byte("image"))
	mw.Close()

	resp, err := http.Post(srv.URL+pushURI, mw.FormDataContentType(), &body)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	monitor := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusAccepted || monitor == "" {
		t.Fatalf("push: status %d, Location %q", resp.StatusCode, monitor)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := http.Get(srv.URL + monitor)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusNoContent {
			break
		}
		if resp.StatusCode != http.StatusAccepted || time.Now().After(deadline) {
			t.Fatalf("task monitor: status %d", resp.StatusCode)
		}
		time.Sleep(5 * time.Millisecond)
	}
	tasks := getJSON(t, srv.URL, "/redfish/v1/TaskService/Tasks")
	members := tasks["Members"].([]any)
	last := members[len(members)-1].(map[string]any)["@odata.id"].(string)
	if task := getJSON(t, srv.URL, last); task["TaskState"] != "Completed" {
		t.Errorf("task = %v", task)
	}
}
