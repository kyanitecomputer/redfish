package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestReadTargets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "targets.txt")
	data := "# comment\nServiceRoot.ServiceRoot\n\n  Chassis.Chassis  # trailing\n#Task.Task\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readTargets(path)
	if want := []string{"ServiceRoot.ServiceRoot", "Chassis.Chassis"}; err != nil || !slices.Equal(got, want) {
		t.Errorf("readTargets = %q, %v, want %q", got, err, want)
	}
}
