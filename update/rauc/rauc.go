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
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"src.kyanite.computer/redfish/gen"
	"src.kyanite.computer/redfish/odata"
	"src.kyanite.computer/redfish/server"
	"src.kyanite.computer/redfish/update"
)

var (
	_ update.Backend   = (*Backend)(nil)
	_ update.Installer = (*Backend)(nil)
	_ update.Validator = (*Backend)(nil)
	_ update.Activator = (*Backend)(nil)
)

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

// Validate implements update.Validator. RAUC always installs into the
// inactive slot and activates it, and takes no credentials for bundle URLs.
// An ImageURI starting with "-" would be read as an option of rauc install.
func (b *Backend) Validate(ctx context.Context, img update.Image, req update.Request) error {
	switch {
	case req.Stage:
		return notSupported("Stage")
	case len(req.ExcludeTargets) > 0:
		return notSupported("ExcludeTargets")
	case img.Username != "" || img.Password != "":
		return notSupported("Username")
	case img.Body == nil && strings.HasPrefix(img.URI, "-"):
		return &server.Error{
			Status:    http.StatusBadRequest,
			MessageID: odata.MsgActionParameterValueFormat,
			Message:   "The value for the parameter ImageURI must not start with '-'.",
		}
	case len(req.Targets) == 0:
		return nil
	}
	slots, _, err := b.slots(ctx)
	if err != nil {
		return err
	}
	for _, t := range req.Targets {
		s, ok := find(slots, t)
		if !ok || s.State == "booted" {
			return &server.Error{
				Status:    http.StatusBadRequest,
				MessageID: odata.MsgActionParameterValueFormat,
				Message:   "The value " + t + " for the parameter Targets is not an inactive RAUC slot of class " + b.cfg.Class + ".",
			}
		}
	}
	return nil
}

// find returns the slot whose firmware inventory item has the @odata.id uri.
func find(slots []namedSlot, uri string) (namedSlot, bool) {
	id, ok := strings.CutPrefix(uri, update.FirmwareInventoryURI+"/")
	if !ok {
		return namedSlot{}, false
	}
	for _, s := range slots {
		if s.id == id {
			return s, true
		}
	}
	return namedSlot{}, false
}

func notSupported(param string) error {
	return &server.Error{
		Status:    http.StatusBadRequest,
		MessageID: odata.MsgActionParameterNotSupported,
		Message:   "The parameter " + param + " is not supported by the RAUC update backend.",
	}
}

// Install implements update.Installer. A pushed bundle is written to a file
// in SpoolDir, which is removed after the installation.
func (b *Backend) Install(ctx context.Context, img update.Image, _ update.Request, progress func(int)) error {
	bundle := img.URI
	if img.Body != nil {
		path, err := b.spool(img.Body)
		if err != nil {
			return err
		}
		defer os.Remove(path)
		bundle = path
	}
	return b.install(ctx, bundle, progress)
}

// spool writes a pushed bundle to a new file in SpoolDir.
func (b *Backend) spool(r io.Reader) (path string, err error) {
	if err := os.MkdirAll(b.cfg.SpoolDir, 0o700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(b.cfg.SpoolDir, "bundle-*.raucb")
	if err != nil {
		return "", err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(f.Name())
		}
	}()
	if _, err := io.Copy(f, r); err != nil {
		return "", fmt.Errorf("receiving bundle: %w", err)
	}
	return f.Name(), nil
}

// install runs "rauc install", reporting the percentages of its progress
// lines ("NN% message"). On failure the error carries RAUC's LastError and
// starts with the name of the command, which need not be rauc: the RSU
// backend runs an installer that reports the same way.
func (b *Backend) install(ctx context.Context, bundle string, progress func(int)) error {
	cmd := exec.CommandContext(ctx, b.cfg.Command, "install", bundle)
	r, w := io.Pipe()
	cmd.Stdout, cmd.Stderr = w, w
	if err := cmd.Start(); err != nil {
		return err
	}

	var lastErr, lastLine string
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if pct, ok := parseProgress(line); ok {
				progress(pct)
			} else if msg, ok := strings.CutPrefix(line, "LastError: "); ok {
				lastErr = msg
			} else if line != "" {
				lastLine = line
			}
		}
		_, _ = io.Copy(io.Discard, r) // unblock rauc after an overlong line
	}()
	err := cmd.Wait()
	w.Close()
	<-done

	if err == nil {
		return nil
	}
	msg := lastErr
	if msg == "" {
		msg = lastLine
	}
	if msg == "" {
		msg = err.Error()
	}
	return errors.New(filepath.Base(b.cfg.Command) + " install: " + msg)
}

// parseProgress parses a "rauc install" progress line such as
// "65% Copying image to rootfs".
func parseProgress(line string) (int, bool) {
	pct, _, ok := strings.Cut(line, "% ")
	if !ok {
		pct, ok = strings.CutSuffix(line, "%")
	}
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(pct)
	return n, err == nil
}

// Activate implements update.Activator: it marks the slot of one firmware
// inventory item as the one to boot next.
func (b *Backend) Activate(ctx context.Context, images, devices []string, _ func(int)) error {
	if len(devices) > 0 {
		return notSupported("Targets")
	}
	if len(images) != 1 {
		return &server.Error{
			Status:    http.StatusBadRequest,
			MessageID: odata.MsgActionParameterValueFormat,
			Message:   "The RAUC update backend activates exactly one slot at a time.",
		}
	}
	slots, _, err := b.slots(ctx)
	if err != nil {
		return err
	}
	s, ok := find(slots, images[0])
	if !ok {
		return &server.Error{
			Status:    http.StatusBadRequest,
			MessageID: odata.MsgActionParameterValueFormat,
			Message:   "The value " + images[0] + " for the parameter Targets is not a RAUC slot of class " + b.cfg.Class + ".",
		}
	}
	_, err = b.run(ctx, "status", "mark-active", s.name)
	return err
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
