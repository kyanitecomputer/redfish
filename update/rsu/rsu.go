// Package rsu is an update.Backend for Intel (Altera) SoC FPGAs updated with
// Remote System Update (RSU), such as Stratix 10 and Agilex, running Linux.
//
// It is a stub: every operation fails with 501 Not Implemented. The intended
// implementation drives the rsu_client tool from Intel's LIBRSU:
//
//   - Inventory: one SoftwareInventory per RSU slot, with Active set for the
//     currently loaded image and the factory image reported as write protected.
//   - Install: write the image to a slot; with Stage set, leave it unrequested.
//   - Activate: request the slot of the given image for the next reconfiguration.
package rsu

import (
	"context"
	"net/http"

	"src.kyanite.computer/redfish/odata"
	"src.kyanite.computer/redfish/server"
	"src.kyanite.computer/redfish/update"
)

var (
	_ update.Backend   = (*Backend)(nil)
	_ update.Installer = (*Backend)(nil)
	_ update.Activator = (*Backend)(nil)
)

var errNotImplemented = &server.Error{
	Status:    http.StatusNotImplemented,
	MessageID: odata.MsgGeneralError,
	Message:   "The Intel RSU update backend is not implemented yet.",
}

// Backend updates the FPGA images with Intel RSU.
type Backend struct{}

// Inventory implements update.Backend.
func (*Backend) Inventory(context.Context) (*update.Inventory, error) {
	return nil, errNotImplemented
}

// Install implements update.Installer.
func (*Backend) Install(context.Context, update.Image, update.Request, func(int)) error {
	return errNotImplemented
}

// Activate implements update.Activator.
func (*Backend) Activate(context.Context, []string, []string, func(int)) error {
	return errNotImplemented
}
