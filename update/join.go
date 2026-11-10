package update

import (
	"context"
	"net/http"
	"slices"

	"src.kyanite.computer/redfish/odata"
	"src.kyanite.computer/redfish/server"
)

var (
	_ Backend   = (*Joined)(nil)
	_ Installer = (*Joined)(nil)
	_ Validator = (*Joined)(nil)
	_ Activator = (*Joined)(nil)
)

// Joined serves the inventory of several backends as one, for example the
// operating system slots of one backend and the FPGA images of another. Their
// inventory items need distinct Ids.
//
// An update or activation goes to the backend whose inventory holds all of its
// targets; an update without targets goes to the first backend that is an
// Installer. Joined always offers updates and activation; they fail with 400 when
// the backend they go to does not support them.
type Joined struct {
	backends []Backend
}

// Join returns a Backend that joins backends.
func Join(backends ...Backend) *Joined {
	return &Joined{backends: backends}
}

// Inventory implements Backend. It fails when one of the backends fails.
func (j *Joined) Inventory(ctx context.Context) (*Inventory, error) {
	all := &Inventory{}
	for _, b := range j.backends {
		inv, err := b.Inventory(ctx)
		if err != nil {
			return nil, err
		}
		if inv != nil {
			all.Firmware = append(all.Firmware, inv.Firmware...)
			all.Software = append(all.Software, inv.Software...)
		}
	}
	return all, nil
}

// owner returns the backend whose inventory holds all of uris.
func (j *Joined) owner(ctx context.Context, uris []string) (Backend, error) {
	for _, b := range j.backends {
		inv, err := b.Inventory(ctx)
		if err != nil {
			return nil, err
		}
		var own []string
		if inv != nil {
			for _, it := range inv.Firmware {
				own = append(own, FirmwareInventoryURI+"/"+it.Id)
			}
			for _, it := range inv.Software {
				own = append(own, SoftwareInventoryURI+"/"+it.Id)
			}
		}
		if !slices.ContainsFunc(uris, func(u string) bool { return !slices.Contains(own, u) }) {
			return b, nil
		}
	}
	return nil, &server.Error{
		Status:    http.StatusBadRequest,
		MessageID: odata.MsgActionParameterValueFormat,
		Message:   "The value for the parameter Targets does not name inventory items of one update backend.",
	}
}

// installer returns the Installer for an update of targets.
func (j *Joined) installer(ctx context.Context, targets []string) (Installer, error) {
	if len(targets) == 0 {
		for _, b := range j.backends {
			if inst, ok := b.(Installer); ok {
				return inst, nil
			}
		}
		return nil, unsupported("update")
	}
	b, err := j.owner(ctx, targets)
	if err != nil {
		return nil, err
	}
	inst, ok := b.(Installer)
	if !ok {
		return nil, unsupported("update")
	}
	return inst, nil
}

// Validate implements Validator.
func (j *Joined) Validate(ctx context.Context, img Image, req Request) error {
	inst, err := j.installer(ctx, req.Targets)
	if err != nil {
		return err
	}
	if v, ok := inst.(Validator); ok {
		return v.Validate(ctx, img, req)
	}
	return nil
}

// Install implements Installer.
func (j *Joined) Install(ctx context.Context, img Image, req Request, progress func(int)) error {
	inst, err := j.installer(ctx, req.Targets)
	if err != nil {
		return err
	}
	return inst.Install(ctx, img, req, progress)
}

// Activate implements Activator.
func (j *Joined) Activate(ctx context.Context, images, devices []string, progress func(int)) error {
	b, err := j.owner(ctx, images)
	if err != nil {
		return err
	}
	act, ok := b.(Activator)
	if !ok {
		return unsupported("activation")
	}
	return act.Activate(ctx, images, devices, progress)
}

func unsupported(what string) error {
	return &server.Error{
		Status:    http.StatusBadRequest,
		MessageID: odata.MsgMethodNotAllowed,
		Message:   "The targets do not support " + what + ".",
	}
}
