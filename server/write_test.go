package server

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"testing/iotest"

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

func TestReadBody(t *testing.T) {
	for _, tt := range []struct {
		size int
		want int // status of the error, 0 for none
	}{
		{0, 0},
		{maxBodyBytes, 0},
		{maxBodyBytes + 1, http.StatusRequestEntityTooLarge},
	} {
		r := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(strings.Repeat(" ", tt.size)))
		body, err := ReadBody(r)
		got := 0
		if e, ok := errors.AsType[*Error](err); ok {
			got = e.Status
		} else if err != nil {
			t.Errorf("%d bytes: error %v is not an *Error", tt.size, err)
		}
		if got != tt.want || err == nil && len(body) != tt.size {
			t.Errorf("%d bytes: read %d, status %d, want %d", tt.size, len(body), got, tt.want)
		}
	}
	r := httptest.NewRequest(http.MethodPatch, "/", iotest.ErrReader(os.ErrDeadlineExceeded))
	if _, err := ReadBody(r); err == nil || err.(*Error).Status != http.StatusRequestTimeout {
		t.Errorf("timed out body: %v, want 408", err)
	}
}

func TestETag(t *testing.T) {
	w := httptest.NewRecorder()
	WriteJSON(w, []byte(`{}`))
	tag := w.Header().Get("ETag")
	if tag == "" {
		t.Fatal("no ETag on a resource")
	}
	w = httptest.NewRecorder()
	WriteError(w, http.StatusNotFound, odata.MsgResourceMissingAtURI, "missing")
	if e := w.Header().Get("ETag"); e != "" {
		t.Errorf("error response has ETag %s", e)
	}
	for _, tt := range []struct {
		ifMatch string
		ok      bool
	}{
		{"", true},
		{"*", true},
		{tag, true},
		{`"other", ` + tag, true},
		{"W/" + tag, false},
		{`"other"`, false},
	} {
		r := httptest.NewRequest(http.MethodPatch, "/", nil)
		if tt.ifMatch != "" {
			r.Header.Set("If-Match", tt.ifMatch)
		}
		if got := CheckIfMatch(r, []byte(`{}`)); got != tt.ok {
			t.Errorf("If-Match %s: %v, want %v", tt.ifMatch, got, tt.ok)
		}
	}
}

func TestStatic(t *testing.T) {
	h := StaticXML("<x/>")
	var tags []string
	for range 2 {
		w := httptest.NewRecorder()
		h(w, httptest.NewRequest(http.MethodGet, "/redfish/v1/$metadata", nil))
		if w.Code != http.StatusOK || w.Body.String() != "<x/>" || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/xml") {
			t.Fatalf("status %d, Content-Type %q, body %q", w.Code, w.Header().Get("Content-Type"), w.Body)
		}
		tags = append(tags, w.Header().Get("ETag"))
	}
	if tags[0] == "" || tags[0] != tags[1] {
		t.Errorf("ETags %q", tags)
	}
}
