package main

import (
	"context"
	"io"

	"src.kyanite.computer/redfish/gen"
	"src.kyanite.computer/redfish/update"
)

// bmcFirmwareID is the firmware inventory Id of the running BMC image, linked
// from the Manager as its active software image.
const bmcFirmwareID = "bmc"

// Inventory implements update.Backend with static firmware and software
// inventory. A real BMC would query its update tooling (for example RAUC slot
// status or the Intel RSU slot table).
func (b *backend) Inventory(_ context.Context) (*update.Inventory, error) {
	return &update.Inventory{
		Firmware: []*gen.SoftwareInventory{
			{
				Id:           bmcFirmwareID,
				Name:         "BMC Firmware",
				Manufacturer: "Kyanite",
				Version:      "1.0.0",
				Updateable:   boolPtr(true),
				Status:       &gen.ResourceStatus{State: gen.ResourceStateEnabled, Health: gen.ResourceHealthOK},
			},
			{
				Id:           "bios",
				Name:         "Host BIOS",
				Manufacturer: "Kyanite",
				Version:      "2026.06",
				Updateable:   boolPtr(true),
				Status:       &gen.ResourceStatus{State: gen.ResourceStateEnabled, Health: gen.ResourceHealthOK},
			},
		},
	}, nil
}

// Install simulates an update: it drains a pushed image and reports progress.
// A real BMC would hand the image to its update tooling.
func (b *backend) Install(_ context.Context, img update.Image, _ update.Request, progress func(int)) error {
	if img.Body != nil {
		if _, err := io.Copy(io.Discard, img.Body); err != nil {
			return err
		}
	}
	for p := 25; p <= 100; p += 25 {
		progress(p)
	}
	return nil
}

// Activate simulates activating staged images; it has nothing to switch.
func (b *backend) Activate(context.Context, []string, []string, func(int)) error {
	return nil
}

func boolPtr(v bool) *bool { return &v }
