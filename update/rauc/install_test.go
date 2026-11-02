package rauc

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"src.kyanite.computer/redfish/server"
	"src.kyanite.computer/redfish/update"
)

const installOK = `installing
  0% Installing
 20% Checking bundle
 40% Checking bundle done.
 65% Copying image to rootfs
 99% Copying image to rootfs done.
100% Installing done.
idle
Installing ` + "`/data/bundle.raucb`" + ` succeeded
`

const installFailed = `installing
  0% Installing
 20% Checking bundle
 40% Checking bundle failed.
100% Installing failed.
LastError: signature verification failed: Verify error: self-signed certificate
idle
Installing ` + "`/data/bundle.raucb`" + ` failed
`

func TestInstallPushed(t *testing.T) {
	f, b := newFakeRAUC(t)
	f.write(t, "install.out", installOK)
	var got []int
	err := b.Install(context.Background(), update.Image{Body: strings.NewReader("bundle-bytes")}, update.Request{},
		func(p int) { got = append(got, p) })
	if err != nil {
		t.Fatal(err)
	}
	if want := []int{0, 20, 40, 65, 99, 100}; !slices.Equal(got, want) {
		t.Errorf("progress = %v, want %v", got, want)
	}
	if data := f.read(t, "installed"); data != "bundle-bytes" {
		t.Errorf("rauc installed %q", data)
	}
	if left, _ := os.ReadDir(filepath.Join(f.dir, "spool")); len(left) != 0 {
		t.Errorf("spool directory not cleaned up: %v", left)
	}
}

func TestInstallURI(t *testing.T) {
	f, b := newFakeRAUC(t)
	err := b.Install(context.Background(), update.Image{URI: "https://fw.example/bundle.raucb"}, update.Request{}, func(int) {})
	if err != nil {
		t.Fatal(err)
	}
	if calls := f.calls(t); calls[len(calls)-1] != "install https://fw.example/bundle.raucb" {
		t.Errorf("calls = %q", calls)
	}
}

func TestInstallFails(t *testing.T) {
	tests := []struct {
		name, out, want string
	}{
		{"LastError", installFailed, "rauc install: signature verification failed: Verify error: self-signed certificate"},
		{"last line", "Failed to contact rauc service\n", "rauc install: Failed to contact rauc service"},
		{"no output", "", "rauc install: exit status 1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, b := newFakeRAUC(t)
			f.write(t, "install.out", tt.out)
			f.write(t, "install.rc", "1")
			err := b.Install(context.Background(), update.Image{Body: strings.NewReader("x")}, update.Request{}, func(int) {})
			if err == nil || err.Error() != tt.want {
				t.Errorf("err = %v, want %q", err, tt.want)
			}
			if left, _ := os.ReadDir(filepath.Join(f.dir, "spool")); len(left) != 0 {
				t.Errorf("spool directory not cleaned up: %v", left)
			}
		})
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

func TestInstallUploadFails(t *testing.T) {
	f, b := newFakeRAUC(t)
	err := b.Install(context.Background(), update.Image{Body: failingReader{}}, update.Request{}, func(int) {})
	if err == nil || !strings.Contains(err.Error(), "connection reset") {
		t.Errorf("err = %v", err)
	}
	if strings.Contains(f.read(t, "calls"), "install") {
		t.Error("rauc install ran on an incomplete upload")
	}
	if left, _ := os.ReadDir(filepath.Join(f.dir, "spool")); len(left) != 0 {
		t.Errorf("spool directory not cleaned up: %v", left)
	}
}

func TestValidate(t *testing.T) {
	_, b := newFakeRAUC(t)
	fw := update.FirmwareInventoryURI + "/"
	tests := []struct {
		name    string
		img     update.Image
		req     update.Request
		wantErr bool
	}{
		{"plain", update.Image{URI: "https://x/b"}, update.Request{}, false},
		{"inactive target", update.Image{URI: "https://x/b"}, update.Request{Targets: []string{fw + "slot_b"}}, false},
		{"booted target", update.Image{URI: "https://x/b"}, update.Request{Targets: []string{fw + "slot_a"}}, true},
		{"unknown target", update.Image{URI: "https://x/b"}, update.Request{Targets: []string{fw + "bios"}}, true},
		{"foreign target", update.Image{URI: "https://x/b"}, update.Request{Targets: []string{"/redfish/v1/Systems/1"}}, true},
		{"stage", update.Image{URI: "https://x/b"}, update.Request{Stage: true}, true},
		{"exclude", update.Image{URI: "https://x/b"}, update.Request{ExcludeTargets: []string{fw + "slot_a"}}, true},
		{"credentials", update.Image{URI: "https://x/b", Username: "u"}, update.Request{}, true},
		{"option-like URI", update.Image{URI: "--help"}, update.Request{}, true},
		{"local path", update.Image{URI: "/data/b.raucb"}, update.Request{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := b.Validate(context.Background(), tt.img, tt.req)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, want error: %v", err, tt.wantErr)
			}
			if se, ok := errors.AsType[*server.Error](err); err != nil && (!ok || se.Status != http.StatusBadRequest) {
				t.Errorf("err = %#v, want a 400 *server.Error", err)
			}
		})
	}
}
