package update

import (
	"context"
	"net/http"
	"slices"
	"testing"
)

type activateCall struct {
	images, devices []string
}

// fakeActivator is an installing backend that also records activations.
type fakeActivator struct {
	*fakeInstaller
	activated chan activateCall
}

func newFakeActivator() *fakeActivator {
	return &fakeActivator{fakeInstaller: newFakeInstaller(), activated: make(chan activateCall, 1)}
}

func (f *fakeActivator) Activate(_ context.Context, images, devices []string, _ func(int)) error {
	f.activated <- activateCall{images, devices}
	return nil
}

func TestActivateAdvertised(t *testing.T) {
	srv := newTestServer(t, newFakeActivator())
	_, m := do(t, "GET", srv.URL+ServiceURI, "")
	actions, _ := m["Actions"].(map[string]any)
	if a, _ := actions["#UpdateService.Activate"].(map[string]any); a["target"] != ActivateURI {
		t.Errorf("UpdateService Actions = %v", actions)
	}
	_, m = do(t, "GET", srv.URL+FirmwareInventoryURI+"/bmc", "")
	actions, _ = m["Actions"].(map[string]any)
	want := FirmwareInventoryURI + "/bmc/Actions/SoftwareInventory.Activate"
	if a, _ := actions["#SoftwareInventory.Activate"].(map[string]any); a["target"] != want {
		t.Errorf("SoftwareInventory Actions = %v, want target %s", actions, want)
	}

	srv = newTestServer(t, newFakeInstaller())
	_, m = do(t, "GET", srv.URL+FirmwareInventoryURI+"/bmc", "")
	if m["Actions"] != nil {
		t.Errorf("Actions without Activator = %v", m["Actions"])
	}
}

func TestActivate(t *testing.T) {
	tests := []struct {
		name        string
		uri, body   string
		wantStatus  int
		wantImages  []string
		wantDevices []string
	}{
		{
			name:       "update service",
			uri:        ActivateURI,
			body:       `{"Targets":[{"@odata.id":"/redfish/v1/UpdateService/FirmwareInventory/bmc"}]}`,
			wantStatus: http.StatusAccepted,
			wantImages: []string{"/redfish/v1/UpdateService/FirmwareInventory/bmc"},
		},
		{
			name:        "inventory item",
			uri:         SoftwareInventoryURI + "/os/Actions/SoftwareInventory.Activate",
			body:        `{"Targets":[{"@odata.id":"/redfish/v1/Systems/1"}]}`,
			wantStatus:  http.StatusAccepted,
			wantImages:  []string{SoftwareInventoryURI + "/os"},
			wantDevices: []string{"/redfish/v1/Systems/1"},
		},
		{
			name:       "inventory item without body",
			uri:        FirmwareInventoryURI + "/bmc/Actions/SoftwareInventory.Activate",
			wantStatus: http.StatusAccepted,
			wantImages: []string{FirmwareInventoryURI + "/bmc"},
		},
		{name: "missing targets", uri: ActivateURI, body: `{}`, wantStatus: http.StatusBadRequest},
		{name: "unknown item", uri: FirmwareInventoryURI + "/nope/Actions/SoftwareInventory.Activate", wantStatus: http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newFakeActivator()
			srv := newTestServer(t, b)
			code, _ := do(t, "POST", srv.URL+tt.uri, tt.body)
			if code != tt.wantStatus {
				t.Fatalf("status %d, want %d", code, tt.wantStatus)
			}
			if code != http.StatusAccepted {
				return
			}
			call := <-b.activated
			if !slices.Equal(call.images, tt.wantImages) || !slices.Equal(call.devices, tt.wantDevices) {
				t.Errorf("Activate(%v, %v), want (%v, %v)", call.images, call.devices, tt.wantImages, tt.wantDevices)
			}
		})
	}
}

func TestActivateWhileInstalling(t *testing.T) {
	b := newFakeActivator()
	srv := newTestServer(t, b)
	if code, _ := do(t, "POST", srv.URL+SimpleUpdateURI, `{"ImageURI":"http://x/i"}`); code != http.StatusAccepted {
		t.Fatalf("SimpleUpdate: status %d", code)
	}
	<-b.got
	if code, _ := do(t, "POST", srv.URL+ActivateURI, `{"Targets":[{"@odata.id":"/x"}]}`); code != http.StatusServiceUnavailable {
		t.Errorf("Activate during install: status %d, want 503", code)
	}
	b.release <- nil
}
