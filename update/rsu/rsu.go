// Package rsu is an update.Backend for Intel (Altera) SoC FPGAs updated with
// Remote System Update (RSU), such as Stratix 10 and Agilex, running Linux. It
// drives rsu_client from Intel's LIBRSU and an installer command:
//
//   - Inventory reports one firmware inventory item per application slot of
//     the sub-partition table (SPT), from "rsu_client --list", and one for the
//     factory image, which is write protected. Partitions named "SSBL.<slot>",
//     which hold U-Boot for a slot, are not reported.
//   - Install runs the installer on a pushed image, spooled to a file first, or
//     on the local path of a SimpleUpdate.
//   - Activate puts a slot at the top of the configuration pointer block (CPB)
//     with "rsu_client --enable" and requests it for the next reconfiguration;
//     for the factory image it only requests it, so a power cycle loads the top
//     slot again.
//
// The installer writes a slot that is not running, the one named by the
// update's target or else one it chooses, and requests it, so staged updates
// and targets other than one slot that is not running are rejected.
package rsu

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"src.kyanite.computer/redfish/gen"
	"src.kyanite.computer/redfish/odata"
	"src.kyanite.computer/redfish/server"
	"src.kyanite.computer/redfish/update"
	"src.kyanite.computer/redfish/update/internal/installer"
)

var (
	_ update.Backend   = (*Backend)(nil)
	_ update.Installer = (*Backend)(nil)
	_ update.Validator = (*Backend)(nil)
	_ update.Activator = (*Backend)(nil)
)

// FactoryID is the inventory Id of the factory image.
const FactoryID = "factory"

// Config configures a Backend. The zero value uses the defaults and cannot
// install images.
type Config struct {
	// Command is the rsu_client executable; "rsu_client" when empty.
	Command string
	// Installer is the command that installs an update image, called as
	// "Installer install [--slot <name>] <file>". It writes the image into
	// the slot named, or else into a slot of its choice, refuses the running
	// slot, requests the slot for the next reconfiguration, and reports like
	// "rauc install": "NN% message" progress lines and "LastError: message"
	// on failure. Without it, updates fail with 501 Not Implemented.
	Installer string
	// SpoolDir holds pushed images while they are installed; it is created if
	// needed. os.TempDir() when empty.
	SpoolDir string
	// Version returns the version of the image in the slot named slot (for
	// the factory image "FACTORY_IM"). Without it, or when it fails, the
	// version is left empty.
	Version func(ctx context.Context, slot string) (string, error)
}

// Backend updates the FPGA images with Intel RSU.
type Backend struct {
	cfg Config
}

// New returns a Backend configured by cfg.
func New(cfg Config) *Backend {
	if cfg.Command == "" {
		cfg.Command = "rsu_client"
	}
	if cfg.SpoolDir == "" {
		cfg.SpoolDir = os.TempDir()
	}
	return &Backend{cfg: cfg}
}

// slot is an application slot of the SPT, numbered as rsu_client numbers it.
type slot struct {
	index    int
	name     string
	offset   uint64
	priority int // 1 is the top of the CPB, 0 is disabled
}

func (s slot) id() string { return strings.ToLower(s.name) }

// state is what rsu_client reports about the slots and the running image.
type state struct {
	slots         []slot
	current, fail uint64
}

// running returns the slot of the running image; false when the factory
// image runs or the image is not in the SPT.
func (st *state) running() (slot, bool) {
	for _, s := range st.slots {
		if s.offset == st.current {
			return s, true
		}
	}
	return slot{}, false
}

func (b *Backend) state(ctx context.Context) (*state, error) {
	out, err := b.run(ctx, "--count")
	if err != nil {
		return nil, err
	}
	count := -1
	for line := range strings.Lines(string(out)) {
		if n, ok := strings.CutPrefix(strings.TrimSpace(line), "number of slots is "); ok {
			count, _ = strconv.Atoi(n)
		}
	}
	if count < 0 {
		return nil, fmt.Errorf("rsu_client --count: unexpected output %q", strings.TrimSpace(string(out)))
	}
	st := &state{}
	for i := range count {
		out, err := b.run(ctx, "--list", strconv.Itoa(i))
		if err != nil {
			return nil, err
		}
		f := fields(out)
		if strings.HasPrefix(f["NAME"], "SSBL.") {
			continue
		}
		s := slot{index: i, name: f["NAME"]}
		if s.offset, err = strconv.ParseUint(f["OFFSET"], 0, 64); err != nil {
			return nil, fmt.Errorf("rsu_client --list %d: bad OFFSET %q", i, f["OFFSET"])
		}
		if p := f["PRIORITY"]; p != "[disabled]" {
			if s.priority, err = strconv.Atoi(p); err != nil {
				return nil, fmt.Errorf("rsu_client --list %d: bad PRIORITY %q", i, p)
			}
		}
		st.slots = append(st.slots, s)
	}
	out, err = b.run(ctx, "--log")
	if err != nil {
		return nil, err
	}
	f := fields(out)
	if st.current, err = strconv.ParseUint(f["CURRENT IMAGE"], 0, 64); err != nil {
		return nil, fmt.Errorf("rsu_client --log: bad CURRENT IMAGE %q", f["CURRENT IMAGE"])
	}
	st.fail, _ = strconv.ParseUint(f["FAIL IMAGE"], 0, 64)
	return st, nil
}

// fields parses "KEY: value" lines.
func fields(out []byte) map[string]string {
	m := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		if k, v, ok := strings.Cut(sc.Text(), ":"); ok {
			m[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return m
}

// Inventory implements update.Backend.
func (b *Backend) Inventory(ctx context.Context) (*update.Inventory, error) {
	st, err := b.state(ctx)
	if err != nil {
		return nil, err
	}
	inv := &update.Inventory{}
	for _, s := range st.slots {
		running := s.offset == st.current
		active := running || s.priority == 1
		updateable := !running
		it := &gen.SoftwareInventory{
			Id:          s.id(),
			Name:        "RSU slot " + s.name,
			Description: fmt.Sprintf("RSU slot %s at %#08x", s.name, s.offset),
			Version:     b.version(ctx, s.name),
			Active:      &active,
			Updateable:  &updateable,
			Status:      &gen.ResourceStatus{State: gen.ResourceStateEnabled, Health: gen.ResourceHealthOK},
		}
		if s.priority == 0 {
			it.Status.State = gen.ResourceStateDisabled
		}
		if s.offset == st.fail {
			it.Status.Health = gen.ResourceHealthCritical
		}
		inv.Firmware = append(inv.Firmware, it)
	}
	_, inSlot := st.running()
	factoryRunning, no, yes := !inSlot, false, true
	inv.Firmware = append(inv.Firmware, &gen.SoftwareInventory{
		Id:             FactoryID,
		Name:           "RSU factory image",
		Description:    "RSU factory image, loaded when no slot can be",
		Version:        b.version(ctx, "FACTORY_IM"),
		Active:         &factoryRunning,
		Updateable:     &no,
		WriteProtected: &yes,
		Status:         &gen.ResourceStatus{State: gen.ResourceStateEnabled},
	})
	return inv, nil
}

func (b *Backend) version(ctx context.Context, slot string) string {
	if b.cfg.Version == nil {
		return ""
	}
	v, err := b.cfg.Version(ctx, slot)
	if err != nil {
		return ""
	}
	return v
}

// Validate implements update.Validator. The installer writes one slot that
// is not running and requests it; it takes a local file and no credentials.
// An ImageURI starting with "-" would be read as an option of the installer.
func (b *Backend) Validate(ctx context.Context, img update.Image, req update.Request) error {
	switch {
	case b.cfg.Installer == "":
		return errNoInstaller
	case req.Stage:
		return notSupported("Stage")
	case len(req.ExcludeTargets) > 0:
		return notSupported("ExcludeTargets")
	case img.Username != "" || img.Password != "":
		return notSupported("Username")
	case img.Body == nil && (strings.Contains(img.URI, "://") || strings.HasPrefix(img.URI, "-")):
		return badValue("The value for the parameter ImageURI must be a local path; the RSU update backend does not download images.")
	case len(req.Targets) == 0:
		return nil
	}
	_, err := b.target(ctx, req.Targets)
	return err
}

// target returns the slot an update of targets goes to: one slot that is
// not running, or none when targets is empty.
func (b *Backend) target(ctx context.Context, targets []string) (*slot, error) {
	if len(targets) == 0 {
		return nil, nil
	}
	if len(targets) > 1 {
		return nil, badValue("The RSU update backend updates exactly one slot at a time.")
	}
	st, err := b.state(ctx)
	if err != nil {
		return nil, err
	}
	for _, s := range st.slots {
		if targets[0] == update.FirmwareInventoryURI+"/"+s.id() && s.offset != st.current {
			return &s, nil
		}
	}
	return nil, badValue("The value " + targets[0] + " for the parameter Targets is not an RSU slot that is not running.")
}

var errNoInstaller = &server.Error{
	Status:    http.StatusNotImplemented,
	MessageID: odata.MsgGeneralError,
	Message:   "The RSU update backend has no installer configured.",
}

func notSupported(param string) error {
	return &server.Error{
		Status:    http.StatusBadRequest,
		MessageID: odata.MsgActionParameterNotSupported,
		Message:   "The parameter " + param + " is not supported by the RSU update backend.",
	}
}

func badValue(msg string) error {
	return &server.Error{
		Status:    http.StatusBadRequest,
		MessageID: odata.MsgActionParameterValueFormat,
		Message:   msg,
	}
}

// Install implements update.Installer. A pushed image is written to a file in
// SpoolDir, which is removed after the installation.
func (b *Backend) Install(ctx context.Context, img update.Image, req update.Request, progress func(int)) error {
	if b.cfg.Installer == "" {
		return errNoInstaller
	}
	target, err := b.target(ctx, req.Targets)
	if err != nil {
		return err
	}
	file := img.URI
	if img.Body != nil {
		path, err := installer.Spool(b.cfg.SpoolDir, "rsu-*.raucb", img.Body)
		if err != nil {
			return err
		}
		defer os.Remove(path)
		file = path
	}
	args := []string{"install"}
	if target != nil {
		args = append(args, "--slot", target.name)
	}
	return installer.Run(ctx, b.cfg.Installer, append(args, file), progress)
}

// Activate implements update.Activator: it makes the slot of one firmware
// inventory item the one to load from the next reconfiguration on.
func (b *Backend) Activate(ctx context.Context, images, devices []string, _ func(int)) error {
	if len(devices) > 0 {
		return notSupported("Targets")
	}
	if len(images) != 1 {
		return badValue("The RSU update backend activates exactly one image at a time.")
	}
	if images[0] == update.FirmwareInventoryURI+"/"+FactoryID {
		_, err := b.run(ctx, "--request-factory")
		return err
	}
	st, err := b.state(ctx)
	if err != nil {
		return err
	}
	for _, s := range st.slots {
		if update.FirmwareInventoryURI+"/"+s.id() == images[0] {
			if _, err := b.run(ctx, "--enable", strconv.Itoa(s.index)); err != nil {
				return err
			}
			_, err := b.run(ctx, "--request", strconv.Itoa(s.index))
			return err
		}
	}
	return badValue("The value " + images[0] + " for the parameter Targets is not an RSU image.")
}

// run runs rsu_client with args and returns its output. rsu_client prints
// its errors to standard output, so the error of a failed command carries
// the last line of its output.
func (b *Backend) run(ctx context.Context, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, b.cfg.Command, args...).CombinedOutput()
	if _, ok := errors.AsType[*exec.ExitError](err); ok {
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		if msg := strings.TrimSpace(lines[len(lines)-1]); msg != "" {
			return nil, fmt.Errorf("rsu_client %s: %s", args[0], msg)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("rsu_client %s: %w", args[0], err)
	}
	return out, nil
}
