// Package rauc is an update.Backend for Linux systems updated with RAUC
// (https://rauc.io).
//
// It is a stub: every operation fails with 501 Not Implemented. The intended
// implementation drives the rauc command line tool:
//
//   - Inventory: one SoftwareInventory per slot from
//     "rauc status --output-format=json", with Active set for the booted slot.
//   - Install: "rauc install" with a pushed bundle spooled to a file, or with
//     the bundle URL for streaming installs.
//   - Activate: "rauc status mark-active" for the slot of the given image.
package rauc

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
	Message:   "The RAUC update backend is not implemented yet.",
}

// Backend updates the system with RAUC.
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
