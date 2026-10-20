package main

import (
	"context"

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

func boolPtr(v bool) *bool { return &v }
