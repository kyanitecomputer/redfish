package rauc

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRAUC is a rauc stand-in: a shell script that logs its arguments to
// calls, answers "status" with status.json, and replays install.out and the
// exit status in install.rc for "install", copying the bundle to installed.
type fakeRAUC struct {
	dir string
}

func newFakeRAUC(t *testing.T) (*fakeRAUC, *Backend) {
	t.Helper()
	f := &fakeRAUC{dir: t.TempDir()}
	status, err := os.ReadFile("testdata/status.json")
	if err != nil {
		t.Fatal(err)
	}
	f.write(t, "status.json", string(status))
	f.write(t, "install.out", "")
	f.write(t, "install.rc", "0")
	script := `#!/bin/sh
D='` + f.dir + `'
echo "$*" >> "$D/calls"
case "$1" in
status)
	if [ "$2" = mark-active ]; then
		echo "rauc mark: marked slot $3 as active"
		exit 0
	fi
	cat "$D/status.json"
	;;
install)
	[ -f "$2" ] && cp "$2" "$D/installed"
	cat "$D/install.out"
	exit "$(cat "$D/install.rc")"
	;;
esac
`
	f.write(t, "rauc", script)
	if err := os.Chmod(filepath.Join(f.dir, "rauc"), 0o755); err != nil {
		t.Fatal(err)
	}
	return f, New(Config{Command: filepath.Join(f.dir, "rauc"), SpoolDir: filepath.Join(f.dir, "spool")})
}

func (f *fakeRAUC) write(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *fakeRAUC) read(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.dir, name))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}

// calls returns the argument lists rauc was called with.
func (f *fakeRAUC) calls(t *testing.T) []string {
	t.Helper()
	return strings.Split(strings.TrimSpace(f.read(t, "calls")), "\n")
}

func TestInventory(t *testing.T) {
	_, b := newFakeRAUC(t)
	inv, err := b.Inventory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Software) != 0 || len(inv.Firmware) != 2 {
		t.Fatalf("inventory = %d firmware, %d software items, want 2 and 0", len(inv.Firmware), len(inv.Software))
	}
	tests := []struct {
		id, name, version, health string
		active, updateable        bool
	}{
		{"slot_b", "Slot B", "hps-v3-40-g4bbbec4", "Critical", false, true},
		{"slot_a", "Slot A", "hps-v3-41-g8dcfb94", "OK", true, false},
	}
	for i, tt := range tests {
		it := inv.Firmware[i]
		if it.Id != tt.id || it.Name != tt.name || it.Version != tt.version || string(it.Status.Health) != tt.health ||
			*it.Active != tt.active || *it.Updateable != tt.updateable || it.SoftwareId != "olix-agilex7-iwave-g43m" {
			t.Errorf("item %d = {%s %q %s %s active=%v updateable=%v %s}, want %+v", i,
				it.Id, it.Name, it.Version, it.Status.Health, *it.Active, *it.Updateable, it.SoftwareId, tt)
		}
	}
}

func TestInventoryOtherClass(t *testing.T) {
	f, _ := newFakeRAUC(t)
	b := New(Config{Command: filepath.Join(f.dir, "rauc"), Class: "boot"})
	inv, err := b.Inventory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Firmware) != 2 || inv.Firmware[0].Id != "boot_0" || inv.Firmware[1].Version != "" {
		t.Errorf("boot inventory = %+v, %+v", inv.Firmware[0], inv.Firmware[1])
	}
}

func TestInventoryRAUCFails(t *testing.T) {
	f, b := newFakeRAUC(t)
	f.write(t, "rauc", "#!/bin/sh\necho 'error creating proxy: Could not connect' >&2\nexit 1\n")
	_, err := b.Inventory(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Could not connect") {
		t.Errorf("err = %v, want rauc's error message", err)
	}
}
