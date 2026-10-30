package rauc

import (
	"context"
	"testing"

	"src.kyanite.computer/redfish/update"
)

func TestActivate(t *testing.T) {
	fw := update.FirmwareInventoryURI + "/"
	tests := []struct {
		name           string
		images, devs   []string
		wantErr        bool
		wantMarkActive string
	}{
		{"inactive slot", []string{fw + "slot_b"}, nil, false, "status mark-active rootfs.1"},
		{"booted slot", []string{fw + "slot_a"}, nil, false, "status mark-active rootfs.0"},
		{"unknown slot", []string{fw + "bios"}, nil, true, ""},
		{"two slots", []string{fw + "slot_a", fw + "slot_b"}, nil, true, ""},
		{"devices", []string{fw + "slot_b"}, []string{"/redfish/v1/Systems/1"}, true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, b := newFakeRAUC(t)
			err := b.Activate(context.Background(), tt.images, tt.devs, func(int) {})
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, want error: %v", err, tt.wantErr)
			}
			calls := f.calls(t)
			last := calls[len(calls)-1]
			if tt.wantMarkActive != "" && last != tt.wantMarkActive {
				t.Errorf("last call = %q, want %q", last, tt.wantMarkActive)
			}
			if tt.wantMarkActive == "" && last != "" && last != "status --detailed --output-format=json" {
				t.Errorf("unexpected call %q", last)
			}
		})
	}
}
