package update

import (
	"cmp"
	"context"
	"net/http"
	"slices"
	"sync"

	"src.kyanite.computer/redfish/odata"
	"src.kyanite.computer/redfish/server"
)

var (
	_ Backend           = (*Joined)(nil)
	_ Installer         = (*Joined)(nil)
	_ Validator         = (*Joined)(nil)
	_ ItemActivator     = (*Joined)(nil)
	_ OptionalInstaller = (*Joined)(nil)
	_ OptionalActivator = (*Joined)(nil)
)

// Joined serves the inventory of several backends as one, for example the
// operating system slots of one backend and the FPGA images of another. Their
// inventory items need distinct Ids; an Id that two backends report belongs
// to the first. A backend whose inventory fails is left out, so the others
// can still be listed and updated, and the joined inventory fails only when
// all fail.
//
// An update or activation goes to the backend whose inventory holds all of its
// targets; an update without targets goes to the first backend that can
// install. The service offers updates and activation only when one of the
// backends supports them, and SoftwareInventory.Activate only on the items of
// a backend that can activate them; a request that goes to a backend that
// does not support it fails with 400.
type Joined struct {
	backends []Backend

	mu     sync.Mutex
	owners map[string]Backend // by @odata.id, as of the last Inventory
}

// Join returns a Backend that joins backends.
func Join(backends ...Backend) *Joined {
	return &Joined{backends: backends}
}

// Inventory implements Backend. A backend that fails leaves its items out,
// so that the others can still be listed and updated; Inventory fails only
// when every backend fails.
func (j *Joined) Inventory(ctx context.Context) (*Inventory, error) {
	all := &Inventory{}
	owners := map[string]Backend{}
	var firstErr error
	failed := 0
	for _, b := range j.backends {
		inv, err := b.Inventory(ctx)
		if err != nil {
			firstErr = cmp.Or(firstErr, err)
			failed++
			continue
		}
		if inv == nil {
			continue
		}
		all.Firmware = append(all.Firmware, inv.Firmware...)
		all.Software = append(all.Software, inv.Software...)
		for _, u := range itemURIs(inv) {
			if _, taken := owners[u]; !taken {
				owners[u] = b
			}
		}
	}
	if failed > 0 && failed == len(j.backends) {
		return nil, firstErr
	}
	j.mu.Lock()
	j.owners = owners
	j.mu.Unlock()
	return all, nil
}

// itemURIs returns the @odata.id of the items of inv.
func itemURIs(inv *Inventory) []string {
	var uris []string
	for _, it := range inv.Firmware {
		uris = append(uris, FirmwareInventoryURI+"/"+it.Id)
	}
	for _, it := range inv.Software {
		uris = append(uris, SoftwareInventoryURI+"/"+it.Id)
	}
	return uris
}

// Installs implements OptionalInstaller: whether one of the backends can
// install.
func (j *Joined) Installs() bool {
	return slices.ContainsFunc(j.backends, func(b Backend) bool {
		_, ok := installerOf(b)
		return ok
	})
}

// Activates implements OptionalActivator: whether one of the backends can
// activate.
func (j *Joined) Activates() bool {
	return slices.ContainsFunc(j.backends, func(b Backend) bool {
		_, ok := activatorOf(b)
		return ok
	})
}

// CanActivate implements ItemActivator: it reports whether the backend of
// image can activate it. It looks the backend up in the owners of the last
// Inventory, which the service lists right before for a GET of an item, and
// asks the backends for their inventories only for an item not listed there.
func (j *Joined) CanActivate(ctx context.Context, image string) bool {
	j.mu.Lock()
	b, ok := j.owners[image]
	j.mu.Unlock()
	if !ok {
		var err error
		if b, err = j.owner(ctx, []string{image}); err != nil {
			return false
		}
	}
	act, ok := activatorOf(b)
	return ok && canActivate(ctx, act, image)
}

// owner returns the first backend whose inventory holds all of uris. A
// backend that fails is passed over; its error is returned only when no
// other backend holds uris, as it might have.
func (j *Joined) owner(ctx context.Context, uris []string) (Backend, error) {
	var firstErr error
	for _, b := range j.backends {
		inv, err := b.Inventory(ctx)
		if err != nil {
			firstErr = cmp.Or(firstErr, err)
			continue
		}
		var own []string
		if inv != nil {
			own = itemURIs(inv)
		}
		if !slices.ContainsFunc(uris, func(u string) bool { return !slices.Contains(own, u) }) {
			return b, nil
		}
	}
	if firstErr != nil {
		return nil, firstErr
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
			if inst, ok := installerOf(b); ok {
				return inst, nil
			}
		}
		return nil, unsupported("update")
	}
	b, err := j.owner(ctx, targets)
	if err != nil {
		return nil, err
	}
	inst, ok := installerOf(b)
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

// Activate implements Activator. It refuses images whose backend cannot
// activate them.
func (j *Joined) Activate(ctx context.Context, images, devices []string, progress func(int)) error {
	b, err := j.owner(ctx, images)
	if err != nil {
		return err
	}
	act, ok := activatorOf(b)
	if !ok || slices.ContainsFunc(images, func(image string) bool { return !canActivate(ctx, act, image) }) {
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
