// Package rauc is an update.Backend for Linux systems updated with RAUC
// (https://rauc.io). It drives the rauc command line tool:
//
//   - Inventory reports one firmware inventory item per slot of one class
//     (by default "rootfs"), from "rauc status --detailed --output-format=json".
//   - Install runs "rauc install" on a pushed bundle, spooled to a file first,
//     or on the URL of a SimpleUpdate, which RAUC streams itself.
//   - Activate runs "rauc status mark-active" on the slot of an inventory item.
//
// RAUC writes the inactive slot and makes it the next to boot, so staged
// updates and explicit targets other than the inactive slot are rejected.
package rauc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"src.kyanite.computer/redfish/gen"
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
	Message:   "Installing and activating with the RAUC update backend is not implemented yet.",
}

// Config configures a Backend. The zero value uses the defaults.
type Config struct {
	// Command is the rauc executable; "rauc" when empty.
	Command string
	// SpoolDir holds pushed bundles while RAUC installs them; it is created
	// if needed. It must have room for a bundle. os.TempDir() when empty.
	SpoolDir string
	// Class is the slot class reported as firmware inventory; "rootfs" when
	// empty.
	Class string
}

// Backend updates the system with RAUC.
type Backend struct {
	cfg Config
}

// New returns a Backend configured by cfg.
func New(cfg Config) *Backend {
	if cfg.Command == "" {
		cfg.Command = "rauc"
	}
	if cfg.SpoolDir == "" {
		cfg.SpoolDir = os.TempDir()
	}
	if cfg.Class == "" {
		cfg.Class = "rootfs"
	}
	return &Backend{cfg: cfg}
}

// status is the output of "rauc status --detailed --output-format=json".
type status struct {
	Compatible string            `json:"compatible"`
	Slots      []map[string]slot `json:"slots"`
}

type slot struct {
	Class      string `json:"class"`
	Device     string `json:"device"`
	Bootname   string `json:"bootname"`
	State      string `json:"state"` // "booted", "active" or "inactive"
	BootStatus string `json:"boot_status"`
	SlotStatus *struct {
		Bundle struct {
			Compatible string `json:"compatible"`
			Version    string `json:"version"`
		} `json:"bundle"`
	} `json:"slot_status"`
}

// namedSlot is a slot of the configured class with its RAUC name and
// inventory Id.
type namedSlot struct {
	slot
	name, id string
}

// slots returns the slots of the configured class in the order RAUC lists
// them.
func (b *Backend) slots(ctx context.Context) ([]namedSlot, string, error) {
	out, err := b.run(ctx, "status", "--detailed", "--output-format=json")
	if err != nil {
		return nil, "", err
	}
	var st status
	if err := json.Unmarshal(out, &st); err != nil {
		return nil, "", fmt.Errorf("rauc status: %w", err)
	}
	var slots []namedSlot
	for _, m := range st.Slots {
		for name, s := range m {
			if s.Class == b.cfg.Class {
				slots = append(slots, namedSlot{slot: s, name: name, id: slotID(name, s.Bootname)})
			}
		}
	}
	return slots, st.Compatible, nil
}

// slotID derives an inventory Id from a slot: "slot_a" for bootname "A",
// otherwise the slot name with dots replaced ("rootfs_0").
func slotID(name, bootname string) string {
	if bootname != "" {
		return "slot_" + strings.ToLower(bootname)
	}
	return strings.ReplaceAll(name, ".", "_")
}

// Inventory implements update.Backend.
func (b *Backend) Inventory(ctx context.Context) (*update.Inventory, error) {
	slots, compatible, err := b.slots(ctx)
	if err != nil {
		return nil, err
	}
	inv := &update.Inventory{}
	for _, s := range slots {
		inv.Firmware = append(inv.Firmware, b.item(s, compatible))
	}
	return inv, nil
}

func (b *Backend) item(s namedSlot, compatible string) *gen.SoftwareInventory {
	name := "Slot " + s.name
	if s.Bootname != "" {
		name = "Slot " + s.Bootname
	}
	booted := s.State == "booted"
	active := booted || s.State == "active"
	updateable := !booted
	it := &gen.SoftwareInventory{
		Id:          s.id,
		Name:        name,
		Description: "RAUC slot " + s.name + " on " + s.Device,
		SoftwareId:  compatible,
		Active:      &active,
		Updateable:  &updateable,
		Status:      &gen.ResourceStatus{State: gen.ResourceStateEnabled},
	}
	if ss := s.SlotStatus; ss != nil {
		it.Version = ss.Bundle.Version
		if ss.Bundle.Compatible != "" {
			it.SoftwareId = ss.Bundle.Compatible
		}
	}
	switch s.BootStatus {
	case "good":
		it.Status.Health = gen.ResourceHealthOK
	case "bad":
		it.Status.Health = gen.ResourceHealthCritical
	}
	return it
}

// Install implements update.Installer. It is not implemented yet.
func (*Backend) Install(context.Context, update.Image, update.Request, func(int)) error {
	return errNotImplemented
}

// Activate implements update.Activator. It is not implemented yet.
func (*Backend) Activate(context.Context, []string, []string, func(int)) error {
	return errNotImplemented
}

// run runs rauc with args and returns its standard output. The error of a
// failed command carries its standard error.
func (b *Backend) run(ctx context.Context, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, b.cfg.Command, args...).Output()
	if ee, ok := errors.AsType[*exec.ExitError](err); ok {
		if msg := strings.TrimSpace(string(ee.Stderr)); msg != "" {
			return nil, fmt.Errorf("rauc %s: %s", args[0], msg)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("rauc %s: %w", args[0], err)
	}
	return out, nil
}
