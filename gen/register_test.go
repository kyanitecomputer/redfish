package gen

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"src.kyanite.computer/redfish/server"
)

// chassis is a ChassisProvider whose first Patch waits for release.
type chassis struct {
	mu      sync.Mutex
	c       Chassis
	patched bool
	inPatch chan struct{}
	release chan struct{}
}

func (f *chassis) GetChassis(context.Context, server.Params) (*Chassis, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.c
	return &c, nil
}

func (f *chassis) PatchChassis(_ context.Context, _ server.Params, patch *ChassisPatch) (*Chassis, error) {
	f.mu.Lock()
	first := !f.patched
	f.patched = true
	f.mu.Unlock()
	if first {
		close(f.inPatch)
		<-f.release
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if patch.AssetTag != nil {
		f.c.AssetTag = *patch.AssetTag
	}
	c := f.c
	return &c, nil
}

// TestPatchIfMatchSerialized sends two PATCHes with the ETag both read: the
// second must not pass If-Match while the first is changing the resource.
func TestPatchIfMatchSerialized(t *testing.T) {
	f := &chassis{c: Chassis{Id: "1"}, inPatch: make(chan struct{}), release: make(chan struct{})}
	mux := http.NewServeMux()
	RegisterChassis(mux, f)
	const uri = "/redfish/v1/Chassis/1"

	get := httptest.NewRecorder()
	mux.ServeHTTP(get, httptest.NewRequest(http.MethodGet, uri, nil))
	etag := get.Header().Get("ETag")
	if get.Code != http.StatusOK || etag == "" {
		t.Fatalf("GET = %d, ETag %q", get.Code, etag)
	}
	patch := func(tag string) <-chan int {
		done := make(chan int, 1)
		go func() {
			req := httptest.NewRequest(http.MethodPatch, uri, strings.NewReader(`{"AssetTag":"`+tag+`"}`))
			req.Header.Set("If-Match", etag)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			done <- w.Code
		}()
		return done
	}
	first := patch("a")
	<-f.inPatch
	second := patch("b")
	// Give the second PATCH the time to get past If-Match, which it must not
	// while the first one runs.
	time.Sleep(20 * time.Millisecond)
	close(f.release)
	if code := <-first; code != http.StatusOK {
		t.Errorf("first PATCH = %d, want 200", code)
	}
	if code := <-second; code != http.StatusPreconditionFailed {
		t.Errorf("second PATCH = %d, want 412", code)
	}
	if f.c.AssetTag != "a" {
		t.Errorf("AssetTag = %q, want a", f.c.AssetTag)
	}
}
