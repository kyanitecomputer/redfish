package installer

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestParseProgress(t *testing.T) {
	for _, tt := range []struct {
		line string
		pct  int
		ok   bool
	}{
		{"65% Copying image to rootfs", 65, true},
		{"100%", 100, true},
		{"0% ", 0, true},
		{"LastError: failed", 0, false},
		{"x% y", 0, false},
		{"", 0, false},
	} {
		if pct, ok := parseProgress(tt.line); pct != tt.pct || ok != tt.ok {
			t.Errorf("parseProgress(%q) = %d, %v, want %d, %v", tt.line, pct, ok, tt.pct, tt.ok)
		}
	}
}

func TestRun(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	for _, tt := range []struct {
		name    string
		args    []string
		want    []int
		wantErr string
	}{
		{"progress", []string{"-c", `echo "10% a"; echo "50% b"`}, []int{10, 50}, ""},
		{"last error", []string{"-c", `echo "20% a"; echo "LastError: boom"; echo other; exit 1`}, []int{20}, "sh -c: boom"},
		{"last line", []string{"-c", `echo first; echo last; exit 1`}, nil, "sh -c: last"},
		{"no output", []string{"-c", `exit 3`}, nil, "sh -c: exit status 3"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var got []int
			err := Run(context.Background(), "/bin/sh", tt.args, func(p int) { got = append(got, p) })
			if (err == nil) != (tt.wantErr == "") || err != nil && err.Error() != tt.wantErr {
				t.Errorf("err = %v, want %q", err, tt.wantErr)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("progress = %v, want %v", got, tt.want)
			}
		})
	}
	// Without arguments the error names the command alone.
	fail := filepath.Join(t.TempDir(), "fail")
	if err := os.WriteFile(fail, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Run(context.Background(), fail, nil, func(int) {}); err == nil || err.Error() != "fail: exit status 1" {
		t.Errorf("Run without arguments = %v", err)
	}
}

func TestSpool(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spool")
	path, err := Spool(dir, "img-*", strings.NewReader("image"))
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "image" {
		t.Errorf("spooled %q, %v", data, err)
	}
	if filepath.Dir(path) != dir {
		t.Errorf("spooled to %s, want in %s", path, dir)
	}
}
