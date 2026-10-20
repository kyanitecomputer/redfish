package update

import (
	"bytes"
	"context"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"src.kyanite.computer/redfish/task"
)

// pushBody builds a multipart push request body from (name, content) pairs.
func pushBody(t *testing.T, parts ...[2]string) (string, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, p := range parts {
		w, err := mw.CreateFormFile(p[0], p[0])
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(p[1]))
	}
	mw.Close()
	return mw.FormDataContentType(), &buf
}

func push(t *testing.T, url, contentType string, body *bytes.Buffer) *http.Response {
	t.Helper()
	resp, err := http.Post(url, contentType, body)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	resp.Body.Close()
	return resp
}

func TestPushAdvertised(t *testing.T) {
	b := newFakeInstaller()
	mux := http.NewServeMux()
	s := New(b, task.New())
	s.MaxImageSizeBytes = 1 << 20
	s.Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	_, m := do(t, "GET", srv.URL+ServiceURI, "")
	if m["MultipartHttpPushUri"] != MultipartPushURI || m["MaxImageSizeBytes"] != float64(1<<20) {
		t.Errorf("UpdateService = %v", m)
	}
}

func TestPush(t *testing.T) {
	b := newFakeInstaller()
	srv := newTestServer(t, b)
	ct, body := pushBody(t,
		[2]string{"UpdateParameters", `{"Targets":["/redfish/v1/UpdateService/FirmwareInventory/bmc"],"ForceUpdate":true}`},
		[2]string{"UpdateFile", "image-bytes"},
	)
	done := make(chan *http.Response)
	go func() { done <- push(t, srv.URL+MultipartPushURI, ct, body) }()

	call := <-b.got
	if call.data != "image-bytes" {
		t.Errorf("image = %q", call.data)
	}
	if len(call.req.Targets) != 1 || !call.req.ForceUpdate || call.req.Stage {
		t.Errorf("request = %+v", call.req)
	}
	resp := <-done
	if resp.StatusCode != http.StatusAccepted || !strings.HasPrefix(resp.Header.Get("Location"), task.MonitorURI+"/") {
		t.Errorf("status %d, Location %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	b.release <- nil
}

// earlyInstaller fails without reading the image.
type earlyInstaller struct{ *fakeBackend }

func (earlyInstaller) Install(context.Context, Image, Request, func(int)) error {
	return errors.New("unsupported image")
}

func TestPushInstallReturnsEarly(t *testing.T) {
	srv := newTestServer(t, earlyInstaller{newFake()})
	ct, body := pushBody(t, [2]string{"UpdateFile", strings.Repeat("x", 1<<20)})
	resp := push(t, srv.URL+MultipartPushURI, ct, body)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status %d, want 202", resp.StatusCode)
	}
}

func TestPushRejected(t *testing.T) {
	tests := []struct {
		name       string
		parts      [][2]string
		rawType    string
		wantStatus int
	}{
		{"not multipart", nil, "application/json", http.StatusBadRequest},
		{"no file", [][2]string{{"UpdateParameters", `{}`}}, "", http.StatusBadRequest},
		{"bad parameters", [][2]string{{"UpdateParameters", `{`}, {"UpdateFile", "x"}}, "", http.StatusBadRequest},
		{"too large", [][2]string{{"UpdateFile", strings.Repeat("x", 4096)}}, "", http.StatusRequestEntityTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newFakeInstaller()
			mux := http.NewServeMux()
			s := New(b, task.New())
			s.MaxImageSizeBytes = 1024
			s.Register(mux)
			srv := httptest.NewServer(mux)
			defer srv.Close()

			ct, body := pushBody(t, tt.parts...)
			if tt.rawType != "" {
				ct, body = tt.rawType, bytes.NewBufferString(`{}`)
			}
			if resp := push(t, srv.URL+MultipartPushURI, ct, body); resp.StatusCode != tt.wantStatus {
				t.Errorf("status %d, want %d", resp.StatusCode, tt.wantStatus)
			}
		})
	}
}
