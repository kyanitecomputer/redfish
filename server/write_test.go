package server

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"src.kyanite.computer/redfish/odata"
)

func TestWriteErr(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantID     string
	}{
		{
			name:       "plain error",
			err:        errors.New("boom"),
			wantStatus: http.StatusInternalServerError,
			wantID:     odata.MsgInternalError,
		},
		{
			name:       "typed error",
			err:        &Error{Status: http.StatusConflict, MessageID: odata.MsgGeneralError, Message: "busy"},
			wantStatus: http.StatusConflict,
			wantID:     odata.MsgGeneralError,
		},
		{
			name:       "wrapped typed error",
			err:        fmt.Errorf("ctx: %w", &Error{Status: http.StatusBadRequest, MessageID: odata.MsgMalformedJSON, Message: "bad"}),
			wantStatus: http.StatusBadRequest,
			wantID:     odata.MsgMalformedJSON,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			WriteErr(rec, tt.err)
			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if body := rec.Body.String(); !strings.Contains(body, `"code":"`+tt.wantID+`"`) {
				t.Errorf("body = %s, want code %s", body, tt.wantID)
			}
		})
	}
}

func TestWriteActionResult(t *testing.T) {
	body := odata.Collection{ODataType: "#T", ODataID: "/x", Name: "x"}
	tests := []struct {
		name         string
		res          *ActionResult
		wantStatus   int
		wantLocation string
		wantBody     bool
	}{
		{"nil", nil, http.StatusNoContent, "", false},
		{"empty", &ActionResult{}, http.StatusNoContent, "", false},
		{"body", &ActionResult{Body: body}, http.StatusOK, "", true},
		{"accepted", &ActionResult{TaskMonitor: "/m/1"}, http.StatusAccepted, "/m/1", false},
		{"accepted with task", &ActionResult{TaskMonitor: "/m/1", Body: body}, http.StatusAccepted, "/m/1", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			WriteActionResult(rec, tt.res)
			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if got := rec.Header().Get("Location"); got != tt.wantLocation {
				t.Errorf("Location = %q, want %q", got, tt.wantLocation)
			}
			if got := rec.Body.Len() > 0; got != tt.wantBody {
				t.Errorf("has body = %v, want %v", got, tt.wantBody)
			}
		})
	}
}
