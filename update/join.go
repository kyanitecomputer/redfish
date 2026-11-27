package update

import (
	"cmp"
	"context"
	"net/http"
	"slices"
	"sync"

	"src.kyanite.computer/redfish/gen"
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
// to the first. A backend whose inventory fails is left out of the joined
// inventory, which fails only when all fail.
//
// An update or activation goes to the backend that owns its targets, the
// first backend that reports each of them; targets of different backends are
// refused with 400. When a backend fails before the owner of every target is
// found, the request fails with its error, as that backend might report one
// of them. An update without targets goes to the first backend that can
// install. An update is validated by the backend it goes to, also when the
// service validated it before. The service offers updates and activation
// only when one of the backends supports them, and SoftwareInventory.Activate
// only on the items of a backend that can activate them; a request that goes
// to a backend that does not support it fails with 400.
type Joined struct {
	backends []Backend
}

// Join returns a Backend that joins backends.
func Join(backends ...Backend) *Joined {
	return &Joined{backends: backends}
}

// Inventory implements Backend. An item whose Id an earlier backend reported
// in the same list is left out. A backend that fails leaves its items out,
// so that the others can still be listed and updated; Inventory fails only
// when every backend fails.
func (j *Joined) Inventory(ctx context.Context) (*Inventory, error) {
	all := &Inventory{}
	var firstErr error
	failed := 0
	for i := range j.backends {
		inv, err := j.inventory(ctx, i)
		if err != nil {
			firstErr = cmp.Or(firstErr, err)
			failed++
			continue
		}
		if inv == nil {
			continue
		}
		all.Firmware = appendNew(all.Firmware, inv.Firmware)
		all.Software = appendNew(all.Software, inv.Software)
	}
	if failed > 0 && failed == len(j.backends) {
		return nil, firstErr
	}
	return all, nil
}

// appendNew appends the items whose Id is not in list yet.
func appendNew(list, items []*gen.SoftwareInventory) []*gen.SoftwareInventory {
	for _, it := range items {
		if !slices.ContainsFunc(list, func(o *gen.SoftwareInventory) bool { return o.Id == it.Id }) {
			list = append(list, it)
		}
	}
	return list
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

// CanActivate implements ItemActivator: it reports whether the owner of
// image can activate it, asking the backends for their inventories.
func (j *Joined) CanActivate(ctx context.Context, image string) bool {
	b, err := j.owner(ctx, []string{image})
	if err != nil {
		return false
	}
	act, ok := activatorOf(b)
	return ok && canActivate(ctx, act, image)
}

// owner returns the backend that owns uris, the first one that reports each
// of them. It fails with 400 when uris belong to different backends or one
// belongs to none, and with a backend's error when that backend fails before
// every URI has its owner.
func (j *Joined) owner(ctx context.Context, uris []string) (Backend, error) {
	pending := slices.Clone(uris)
	owner := -1
	for i := range j.backends {
		if len(pending) == 0 {
			break
		}
		inv, err := j.inventory(ctx, i)
		if err != nil {
			return nil, err
		}
		var own []string
		if inv != nil {
			own = itemURIs(inv)
		}
		n := len(pending)
		pending = slices.DeleteFunc(pending, func(u string) bool { return slices.Contains(own, u) })
		if len(pending) == n {
			continue
		}
		if owner >= 0 {
			return nil, badTargets
		}
		owner = i
	}
	if owner < 0 || len(pending) > 0 {
		return nil, badTargets
	}
	return j.backends[owner], nil
}

// inventories holds the inventories of the backends of Joined values read
// for one request, so that the request reads each once, however often it
// lists the inventory and looks up the owner of an item.
type inventories struct {
	mu sync.Mutex
	m  map[*Joined][]*inventoryResult
}

type inventoryResult struct {
	inv *Inventory
	err error
}

type inventoriesKey struct{}

// withInventories returns ctx with a new inventories, unless it has one.
// The service gives it to the backend calls of a request; tasks run
// without, so that they see the current inventory.
func withInventories(ctx context.Context) context.Context {
	if _, ok := ctx.Value(inventoriesKey{}).(*inventories); ok {
		return ctx
	}
	return context.WithValue(ctx, inventoriesKey{}, &inventories{m: map[*Joined][]*inventoryResult{}})
}

// inventory returns the inventory of backend i, read once per inventories
// in ctx.
func (j *Joined) inventory(ctx context.Context, i int) (*Inventory, error) {
	c, ok := ctx.Value(inventoriesKey{}).(*inventories)
	if !ok {
		return j.backends[i].Inventory(ctx)
	}
	c.mu.Lock()
	rs := c.m[j]
	if rs == nil {
		rs = make([]*inventoryResult, len(j.backends))
		c.m[j] = rs
	}
	r := rs[i]
	c.mu.Unlock()
	if r != nil {
		return r.inv, r.err
	}
	inv, err := j.backends[i].Inventory(ctx)
	c.mu.Lock()
	rs[i] = &inventoryResult{inv, err}
	c.mu.Unlock()
	return inv, err
}

var badTargets = &server.Error{
	Status:    http.StatusBadRequest,
	MessageID: odata.MsgActionParameterValueFormatError,
	Message:   "The value for the parameter Targets does not name inventory items of one update backend.",
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

// Install implements Installer. It validates the update with the backend it
// goes to, which may not be the one Validate found when an inventory changed
// in between.
func (j *Joined) Install(ctx context.Context, img Image, req Request, progress func(int)) error {
	inst, err := j.installer(ctx, req.Targets)
	if err != nil {
		return err
	}
	if v, ok := inst.(Validator); ok {
		if err := v.Validate(ctx, img, req); err != nil {
			return err
		}
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
		MessageID: odata.MsgActionNotSupported,
		Message:   "The targets do not support " + what + ".",
	}
}
