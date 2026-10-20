package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandle(t *testing.T) {
	mux := http.NewServeMux()
	var got Params
	h := func(w http.ResponseWriter, r *http.Request, p Params) {
		got = p
		w.WriteHeader(http.StatusOK)
	}
	Handle(mux, "GET", "/redfish/v1/", h)
	Handle(mux, "GET", "/redfish/v1/Inventory/{Id}", h)
	Handle(mux, "GET", "/redfish/v1/Other/{Id}", h)

	tests := []struct {
		path       string
		wantStatus int
		wantURI    string
		wantPath   string
		wantID     string
	}{
		{"/redfish/v1/", http.StatusOK, "/redfish/v1/", "/redfish/v1", ""},
		{"/redfish/v1/Inventory/bmc", http.StatusOK, "/redfish/v1/Inventory/{Id}", "/redfish/v1/Inventory/bmc", "bmc"},
		{"/redfish/v1/Other/bios", http.StatusOK, "/redfish/v1/Other/{Id}", "/redfish/v1/Other/bios", "bios"},
		{"/redfish/v1/Unknown", http.StatusNotFound, "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got = Params{}
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest("GET", tt.path, nil))
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if got.URI != tt.wantURI || got.Path() != tt.wantPath || got.Get("Id") != tt.wantID {
				t.Errorf("params = {URI: %q, Path: %q, Id: %q}, want {%q, %q, %q}",
					got.URI, got.Path(), got.Get("Id"), tt.wantURI, tt.wantPath, tt.wantID)
			}
		})
	}
}
