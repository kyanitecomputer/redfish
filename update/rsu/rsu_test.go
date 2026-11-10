package rsu

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"src.kyanite.computer/redfish/server"
	"src.kyanite.computer/redfish/update"
)

// fakeRSU is an rsu_client stand-in: a shell script that logs its arguments
// to calls, answers --count, --list and --log from the files count, list<n>
// and log, and fails --enable and --request when the file fail exists.
// installer is an installer stand-in that replays install.out.
type fakeRSU struct {
	dir string
}

// The SPT of an Agilex 7 with a factory image, P1 and P2, and U-Boot for
// each in an SSBL.<image> partition; P1 runs and is on top of the CPB, P2
// failed.
var spt = []struct{ name, offset, priority string }{
	{"P1", "0x0000000002000000", "1"},
	{"P2", "0x0000000003000000", "2"},
	{"SSBL.P2", "0x0000000003E00000", "[disabled]"},
	{"SSBL.P1", "0x0000000002E00000", "[disabled]"},
	{"SSBL.FACTORY_IM", "0x0000000000E00000", "[disabled]"},
}

const logP1 = `      VERSION: 0x00000202
        STATE: 0x00000000
CURRENT IMAGE: 0x0000000002000000
   FAIL IMAGE: 0x0000000003000000
    ERROR LOC: 0x00000000
ERROR DETAILS: 0x00000000
RETRY COUNTER: 0x00000000
Operation completed
`

func newFakeRSU(t *testing.T) (*fakeRSU, *Backend) {
	t.Helper()
	f := &fakeRSU{dir: t.TempDir()}
	f.write(t, "count", fmt.Sprintf("number of slots is %d\nOperation completed\n", len(spt)))
	for i, s := range spt {
		f.write(t, fmt.Sprintf("list%d", i), fmt.Sprintf(
			"      NAME: %s\n    OFFSET: %s\n      SIZE: 0x00E00000\n  PRIORITY: %s\nOperation completed\n",
			s.name, s.offset, s.priority))
	}
	f.write(t, "log", logP1)
	f.script(t, "rsu_client", `
echo "$*" >> "$D/calls"
case "$1" in
--count) cat "$D/count" ;;
--list) cat "$D/list$2" ;;
--log) cat "$D/log" ;;
--enable|--request|--request-factory)
	if [ -f "$D/fail" ]; then
		echo "ERROR: $(cat "$D/fail")"
		exit 1
	fi
	echo "Operation completed"
	;;
*) exit 1 ;;
esac
`)
	f.write(t, "install.out", "  0% Checking bundle\n100% Installing done.\n")
	f.write(t, "install.rc", "0")
	f.script(t, "installer", `
echo "installer $*" >> "$D/calls"
[ -f "$2" ] && cp "$2" "$D/installed"
cat "$D/install.out"
exit "$(cat "$D/install.rc")"
`)
	return f, New(Config{
		Command:   filepath.Join(f.dir, "rsu_client"),
		Installer: filepath.Join(f.dir, "installer"),
		SpoolDir:  filepath.Join(f.dir, "spool"),
		Version: func(_ context.Context, slot string) (string, error) {
			if slot == "P1" {
				return "hps-v4-2-g1234567", nil
			}
			return "", errors.New("no version")
		},
	})
}

func (f *fakeRSU) script(t *testing.T, name, body string) {
	t.Helper()
	f.write(t, name, "#!/bin/sh\nD='"+f.dir+"'\n"+body)
	if err := os.Chmod(filepath.Join(f.dir, name), 0o755); err != nil {
		t.Fatal(err)
	}
}

func (f *fakeRSU) write(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *fakeRSU) read(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.dir, name))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}

// writes returns the calls that change the flash or the next image.
func (f *fakeRSU) writes(t *testing.T) []string {
	t.Helper()
	var w []string
	for line := range strings.Lines(f.read(t, "calls")) {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "--count") && !strings.HasPrefix(line, "--list") && line != "--log" {
			w = append(w, line)
		}
	}
	return w
}

const fw = update.FirmwareInventoryURI + "/"

func TestInventory(t *testing.T) {
	_, b := newFakeRSU(t)
	inv, err := b.Inventory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Software) != 0 || len(inv.Firmware) != 3 {
		t.Fatalf("inventory = %d firmware, %d software items, want 3 and 0", len(inv.Firmware), len(inv.Software))
	}
	tests := []struct {
		id, name, version, state, health string
		active, updateable, protected    bool
	}{
		{"p1", "RSU slot P1", "hps-v4-2-g1234567", "Enabled", "OK", true, false, false},
		{"p2", "RSU slot P2", "", "Enabled", "Critical", false, true, false},
		{"factory", "RSU factory image", "", "Enabled", "", false, false, true},
	}
	for i, tt := range tests {
		it := inv.Firmware[i]
		protected := it.WriteProtected != nil && *it.WriteProtected
		if it.Id != tt.id || it.Name != tt.name || it.Version != tt.version || string(it.Status.State) != tt.state ||
			string(it.Status.Health) != tt.health || *it.Active != tt.active || *it.Updateable != tt.updateable ||
			protected != tt.protected {
			t.Errorf("item %d = {%s %q %q %s %s active=%v updateable=%v protected=%v}, want %+v", i, it.Id, it.Name,
				it.Version, it.Status.State, it.Status.Health, *it.Active, *it.Updateable, protected, tt)
		}
	}
	if d := inv.Firmware[1].Description; d != "RSU slot P2 at 0x03000000" {
		t.Errorf("description = %q", d)
	}
}

func TestInventoryFactoryRunning(t *testing.T) {
	f, b := newFakeRSU(t)
	f.write(t, "log", strings.Replace(logP1, "CURRENT IMAGE: 0x0000000002000000", "CURRENT IMAGE: 0x0000000000210000", 1))
	f.write(t, "list1", "NAME: P2\nOFFSET: 0x0000000003000000\nPRIORITY: [disabled]\n")
	inv, err := b.Inventory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	p1, p2, factory := inv.Firmware[0], inv.Firmware[1], inv.Firmware[2]
	if !*p1.Active || !*p1.Updateable || *p2.Active || p2.Status.State != "Disabled" || !*factory.Active {
		t.Errorf("p1 active=%v updateable=%v, p2 active=%v %s, factory active=%v",
			*p1.Active, *p1.Updateable, *p2.Active, p2.Status.State, *factory.Active)
	}
}

func TestInventoryRSUClientFails(t *testing.T) {
	f, b := newFakeRSU(t)
	f.script(t, "rsu_client", "echo 'ERROR: librsu_init: Failed to initialize'\nexit 1\n")
	_, err := b.Inventory(context.Background())
	if err == nil || err.Error() != "rsu_client --count: ERROR: librsu_init: Failed to initialize" {
		t.Errorf("err = %v, want rsu_client's message", err)
	}
}

func TestInstallPushed(t *testing.T) {
	f, b := newFakeRSU(t)
	var got []int
	err := b.Install(context.Background(), update.Image{Body: strings.NewReader("image-bytes")}, update.Request{},
		func(p int) { got = append(got, p) })
	if err != nil {
		t.Fatal(err)
	}
	if want := []int{0, 100}; !slices.Equal(got, want) {
		t.Errorf("progress = %v, want %v", got, want)
	}
	if data := f.read(t, "installed"); data != "image-bytes" {
		t.Errorf("installer got %q", data)
	}
	if left, _ := os.ReadDir(filepath.Join(f.dir, "spool")); len(left) != 0 {
		t.Errorf("spool directory not cleaned up: %v", left)
	}
}

func TestInstallFails(t *testing.T) {
	f, b := newFakeRSU(t)
	f.write(t, "install.out", "  0% Checking bundle\nLastError: bundle is for 'a', this system takes 'b'\n")
	f.write(t, "install.rc", "1")
	err := b.Install(context.Background(), update.Image{URI: "/data/x.raucb"}, update.Request{}, func(int) {})
	if err == nil || err.Error() != "installer install: bundle is for 'a', this system takes 'b'" {
		t.Errorf("err = %v", err)
	}
	if w := f.writes(t); !slices.Equal(w, []string{"installer install /data/x.raucb"}) {
		t.Errorf("calls = %q", w)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name   string
		img    update.Image
		req    update.Request
		status int
	}{
		{"pushed", update.Image{Body: strings.NewReader("")}, update.Request{}, 0},
		{"local path", update.Image{URI: "/data/rsu.raucb"}, update.Request{}, 0},
		{"free slot", update.Image{URI: "/data/rsu.raucb"}, update.Request{Targets: []string{fw + "p2"}}, 0},
		{"running slot", update.Image{URI: "/data/rsu.raucb"}, update.Request{Targets: []string{fw + "p1"}}, 400},
		{"two slots", update.Image{URI: "/data/rsu.raucb"}, update.Request{Targets: []string{fw + "p2", fw + "p2"}}, 400},
		{"factory", update.Image{URI: "/data/rsu.raucb"}, update.Request{Targets: []string{fw + "factory"}}, 400},
		{"URL", update.Image{URI: "https://fw.example/rsu.raucb"}, update.Request{}, 400},
		{"option", update.Image{URI: "--help"}, update.Request{}, 400},
		{"stage", update.Image{URI: "/data/rsu.raucb"}, update.Request{Stage: true}, 400},
		{"exclude", update.Image{URI: "/data/rsu.raucb"}, update.Request{ExcludeTargets: []string{fw + "p1"}}, 400},
		{"credentials", update.Image{URI: "/data/rsu.raucb", Username: "u"}, update.Request{}, 400},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, b := newFakeRSU(t)
			err := b.Validate(context.Background(), tt.img, tt.req)
			if tt.status == 0 {
				if err != nil {
					t.Errorf("err = %v", err)
				}
				return
			}
			if se, ok := errors.AsType[*server.Error](err); !ok || se.Status != tt.status {
				t.Errorf("err = %v, want status %d", err, tt.status)
			}
		})
	}
}

func TestInstallTarget(t *testing.T) {
	// From the factory image both slots are free; the target chooses one.
	f, b := newFakeRSU(t)
	f.write(t, "log", strings.Replace(logP1, "0x0000000002000000", "0x0000000000210000", 1))
	req := update.Request{Targets: []string{fw + "p1"}}
	if err := b.Validate(context.Background(), update.Image{URI: "/data/rsu.raucb"}, req); err != nil {
		t.Fatal(err)
	}
	if err := b.Install(context.Background(), update.Image{URI: "/data/rsu.raucb"}, req, func(int) {}); err != nil {
		t.Fatal(err)
	}
	if w := f.writes(t); !slices.Equal(w, []string{"installer install --slot P1 /data/rsu.raucb"}) {
		t.Errorf("calls = %q", w)
	}
}

func TestInstallRunningSlot(t *testing.T) {
	f, b := newFakeRSU(t)
	err := b.Install(context.Background(), update.Image{URI: "/data/rsu.raucb"},
		update.Request{Targets: []string{fw + "p1"}}, func(int) {})
	if se, ok := errors.AsType[*server.Error](err); !ok || se.Status != http.StatusBadRequest {
		t.Errorf("err = %v, want 400", err)
	}
	if w := f.writes(t); len(w) != 0 {
		t.Errorf("calls = %q", w)
	}
}

func TestNoInstaller(t *testing.T) {
	f, _ := newFakeRSU(t)
	b := New(Config{Command: filepath.Join(f.dir, "rsu_client")})
	for _, err := range []error{
		b.Validate(context.Background(), update.Image{URI: "/data/rsu.raucb"}, update.Request{}),
		b.Install(context.Background(), update.Image{URI: "/data/rsu.raucb"}, update.Request{}, func(int) {}),
	} {
		if se, ok := errors.AsType[*server.Error](err); !ok || se.Status != http.StatusNotImplemented {
			t.Errorf("err = %v, want 501", err)
		}
	}
}

func TestActivate(t *testing.T) {
	tests := []struct {
		name        string
		images      []string
		devices     []string
		wantErr     bool
		wantWrites  []string
		failRequest bool
	}{
		{"slot", []string{fw + "p2"}, nil, false, []string{"--enable 1", "--request 1"}, false},
		{"running slot", []string{fw + "p1"}, nil, false, []string{"--enable 0", "--request 0"}, false},
		{"factory", []string{fw + "factory"}, nil, false, []string{"--request-factory"}, false},
		{"bare factory id", []string{"factory"}, nil, true, nil, false},
		{"SSBL partition", []string{fw + "ssbl.p2"}, nil, true, nil, false},
		{"two images", []string{fw + "p1", fw + "p2"}, nil, true, nil, false},
		{"devices", []string{fw + "p2"}, []string{"/redfish/v1/Systems/1"}, true, nil, false},
		{"rsu_client fails", []string{fw + "p2"}, nil, true, []string{"--enable 1"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, b := newFakeRSU(t)
			if tt.failRequest {
				f.write(t, "fail", "slot is empty")
			}
			err := b.Activate(context.Background(), tt.images, tt.devices, func(int) {})
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, want error: %v", err, tt.wantErr)
			}
			if tt.failRequest && (err == nil || err.Error() != "rsu_client --enable: ERROR: slot is empty") {
				t.Errorf("err = %v", err)
			}
			if w := f.writes(t); !slices.Equal(w, tt.wantWrites) {
				t.Errorf("writes = %q, want %q", w, tt.wantWrites)
			}
		})
	}
}
