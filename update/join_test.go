package update

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"testing"

	"src.kyanite.computer/redfish/gen"
	"src.kyanite.computer/redfish/server"
)

// joinPart is a backend for the Join tests that records the calls it gets;
// validate is returned by Validate.
type joinPart struct {
	name     string
	firmware []string
	calls    *[]string
	validate error
}

func (p *joinPart) Inventory(context.Context) (*Inventory, error) {
	inv := &Inventory{}
	for _, id := range p.firmware {
		inv.Firmware = append(inv.Firmware, &gen.SoftwareInventory{Id: id})
	}
	return inv, nil
}

func (p *joinPart) Validate(context.Context, Image, Request) error {
	*p.calls = append(*p.calls, p.name+" validate")
	return p.validate
}

func (p *joinPart) Install(context.Context, Image, Request, func(int)) error {
	*p.calls = append(*p.calls, p.name+" install")
	return nil
}

func (p *joinPart) Activate(_ context.Context, images, _ []string, _ func(int)) error {
	*p.calls = append(*p.calls, p.name+" activate")
	return nil
}

// inventoryOnly is a backend that neither installs nor activates.
type inventoryOnly struct{ id string }

func (b inventoryOnly) Inventory(context.Context) (*Inventory, error) {
	return &Inventory{Software: []*gen.SoftwareInventory{{Id: b.id}}}, nil
}

func newJoin() (*Joined, *[]string) {
	calls := &[]string{}
	return Join(
		inventoryOnly{"os"},
		&joinPart{name: "rauc", firmware: []string{"slot_a", "slot_b"}, calls: calls},
		&joinPart{name: "rsu", firmware: []string{"p1", "p2"}, calls: calls,
			validate: &server.Error{Status: http.StatusBadRequest}},
	), calls
}

func TestJoinInventory(t *testing.T) {
	j, _ := newJoin()
	inv, err := j.Inventory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, it := range append(inv.Firmware, inv.Software...) {
		ids = append(ids, it.Id)
	}
	if want := []string{"slot_a", "slot_b", "p1", "p2", "os"}; !slices.Equal(ids, want) {
		t.Errorf("ids = %v, want %v", ids, want)
	}
}

func TestJoinRoutes(t *testing.T) {
	fw := FirmwareInventoryURI + "/"
	tests := []struct {
		name    string
		do      func(context.Context, *Joined) error
		status  int
		wantLog []string
	}{
		{"install without targets", func(ctx context.Context, j *Joined) error {
			return j.Install(ctx, Image{}, Request{}, func(int) {})
		}, 0, []string{"rauc validate", "rauc install"}},
		{"install validated by the second backend", func(ctx context.Context, j *Joined) error {
			return j.Install(ctx, Image{}, Request{Targets: []string{fw + "p2"}}, func(int) {})
		}, http.StatusBadRequest, []string{"rsu validate"}},
		{"validate by the owner", func(ctx context.Context, j *Joined) error {
			return j.Validate(ctx, Image{}, Request{Targets: []string{fw + "p1"}})
		}, http.StatusBadRequest, []string{"rsu validate"}},
		{"targets of two backends", func(ctx context.Context, j *Joined) error {
			return j.Validate(ctx, Image{}, Request{Targets: []string{fw + "slot_b", fw + "p2"}})
		}, http.StatusBadRequest, nil},
		{"unknown target", func(ctx context.Context, j *Joined) error {
			return j.Install(ctx, Image{}, Request{Targets: []string{fw + "bios"}}, func(int) {})
		}, http.StatusBadRequest, nil},
		{"target without installer", func(ctx context.Context, j *Joined) error {
			return j.Validate(ctx, Image{}, Request{Targets: []string{SoftwareInventoryURI + "/os"}})
		}, http.StatusBadRequest, nil},
		{"activate", func(ctx context.Context, j *Joined) error {
			return j.Activate(ctx, []string{fw + "slot_a"}, nil, func(int) {})
		}, 0, []string{"rauc activate"}},
		{"activate without activator", func(ctx context.Context, j *Joined) error {
			return j.Activate(ctx, []string{SoftwareInventoryURI + "/os"}, nil, func(int) {})
		}, http.StatusBadRequest, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			j, calls := newJoin()
			err := tt.do(context.Background(), j)
			if tt.status == 0 && err != nil {
				t.Errorf("err = %v", err)
			}
			if se, ok := errors.AsType[*server.Error](err); tt.status != 0 && (!ok || se.Status != tt.status) {
				t.Errorf("err = %v, want status %d", err, tt.status)
			}
			if !slices.Equal(*calls, tt.wantLog) {
				t.Errorf("calls = %v, want %v", *calls, tt.wantLog)
			}
		})
	}
}

// TestJoinAdvertises checks that the service offers Activate only on items of
// a backend that can activate them, and no update actions at all when no
// backend supports them.
func TestJoinAdvertises(t *testing.T) {
	j, _ := newJoin()
	srv := newTestServer(t, j)
	for _, tt := range []struct {
		uri      string
		activate bool
	}{
		{FirmwareInventoryURI + "/slot_a", true},
		{FirmwareInventoryURI + "/p2", true},
		{SoftwareInventoryURI + "/os", false},
	} {
		_, m := do(t, "GET", srv.URL+tt.uri, "")
		actions, _ := m["Actions"].(map[string]any)
		if _, ok := actions["#SoftwareInventory.Activate"]; ok != tt.activate {
			t.Errorf("%s: Activate advertised = %v, want %v", tt.uri, ok, tt.activate)
		}
	}
	code, _ := do(t, "POST", srv.URL+SoftwareInventoryURI+"/os/Actions/SoftwareInventory.Activate", `{}`)
	if code != http.StatusBadRequest {
		t.Errorf("Activate of os: status %d, want 400", code)
	}

	srv = newTestServer(t, Join(inventoryOnly{"os"}, inventoryOnly{"bios"}))
	_, m := do(t, "GET", srv.URL+ServiceURI, "")
	if _, ok := m["Actions"]; ok || m["MultipartHttpPushUri"] != nil {
		t.Errorf("UpdateService of inventory-only backends = %v, want no actions", m)
	}
	if code, _ := do(t, "POST", srv.URL+SimpleUpdateURI, `{"ImageURI":"http://x/i"}`); code == http.StatusAccepted {
		t.Error("SimpleUpdate accepted without an installer")
	}
}

// itemActivator activates only the items in can.
type itemActivator struct {
	joinPart
	can []string
}

func (a *itemActivator) CanActivate(_ context.Context, image string) bool {
	return slices.Contains(a.can, image)
}

func TestJoinItemActivator(t *testing.T) {
	calls := &[]string{}
	fw := FirmwareInventoryURI + "/"
	j := Join(&itemActivator{
		joinPart: joinPart{name: "rsu", firmware: []string{"p1", "factory"}, calls: calls},
		can:      []string{fw + "p1"},
	})
	ctx := context.Background()
	// Without an Inventory first, Joined asks the backends.
	if !j.CanActivate(ctx, fw+"p1") || j.CanActivate(ctx, fw+"factory") || j.CanActivate(ctx, fw+"bios") {
		t.Error("CanActivate does not follow the backend")
	}
	if _, err := j.Inventory(ctx); err != nil {
		t.Fatal(err)
	}
	if !j.CanActivate(ctx, fw+"p1") || j.CanActivate(ctx, fw+"factory") {
		t.Error("CanActivate after Inventory does not follow the backend")
	}

	// Neither Joined nor the service's UpdateService.Activate activates an
	// item that CanActivate refuses.
	err := j.Activate(ctx, []string{fw + "factory"}, nil, func(int) {})
	if se, ok := errors.AsType[*server.Error](err); !ok || se.Status != http.StatusBadRequest {
		t.Errorf("Activate of factory: err = %v, want 400", err)
	}
	srv := newTestServer(t, j)
	code, m := do(t, "POST", srv.URL+ActivateURI, `{"Targets":[{"@odata.id":"`+fw+`factory"}]}`)
	if code != http.StatusBadRequest || errMessage(m) != unsupportedActivation {
		t.Errorf("UpdateService.Activate of factory: status %d, message %q; want 400, %q", code, errMessage(m), unsupportedActivation)
	}
	if len(*calls) != 0 {
		t.Errorf("calls = %v, want none", *calls)
	}
	if err := j.Activate(ctx, []string{fw + "p1"}, nil, func(int) {}); err != nil || !slices.Equal(*calls, []string{"rsu activate"}) {
		t.Errorf("Activate of p1: err = %v, calls = %v", err, *calls)
	}
}

// unconfigured is a backend that implements Installer but cannot install.
type unconfigured struct{ joinPart }

func (unconfigured) Installs() bool { return false }

func TestJoinOptionalInstaller(t *testing.T) {
	calls := &[]string{}
	fw := FirmwareInventoryURI + "/"
	j := Join(
		&unconfigured{joinPart{name: "rsu", firmware: []string{"p1"}, calls: calls}},
		&joinPart{name: "rauc", firmware: []string{"slot_a"}, calls: calls},
	)
	ctx := context.Background()
	// An update without targets skips the backend that cannot install.
	if err := j.Install(ctx, Image{}, Request{}, func(int) {}); err != nil || !slices.Equal(*calls, []string{"rauc validate", "rauc install"}) {
		t.Errorf("install without targets: err = %v, calls = %v", err, *calls)
	}
	err := j.Install(ctx, Image{}, Request{Targets: []string{fw + "p1"}}, func(int) {})
	if se, ok := errors.AsType[*server.Error](err); !ok || se.Status != http.StatusBadRequest {
		t.Errorf("install into p1: err = %v, want 400", err)
	}

	// Without a backend that can install, no update is offered.
	j = Join(&unconfigured{joinPart{name: "rsu", firmware: []string{"p1"}, calls: calls}}, inventoryOnly{"os"})
	if j.Installs() {
		t.Error("Installs without a backend that can install")
	}
	srv := newTestServer(t, j)
	_, m := do(t, "GET", srv.URL+ServiceURI, "")
	if actions, _ := m["Actions"].(map[string]any); actions["#UpdateService.SimpleUpdate"] != nil || m["MultipartHttpPushUri"] != nil {
		t.Errorf("UpdateService = %v, want no update actions", m)
	}
}

// failing is a backend whose inventory fails.
type failing struct{}

func (failing) Inventory(context.Context) (*Inventory, error) {
	return nil, errors.New("rsu_client missing")
}

// TestJoinFailingBackend checks that a failing backend leaves the others
// listed, and updated when it comes after their owner, but that a target is
// not routed past it, as it might report the target.
func TestJoinFailingBackend(t *testing.T) {
	calls := &[]string{}
	fw := FirmwareInventoryURI + "/"
	rauc := &joinPart{name: "rauc", firmware: []string{"slot_a"}, calls: calls}
	ctx := context.Background()

	j := Join(rauc, failing{})
	inv, err := j.Inventory(ctx)
	if err != nil || len(inv.Firmware) != 1 || inv.Firmware[0].Id != "slot_a" {
		t.Fatalf("inventory = %v, %v; want slot_a", inv, err)
	}
	if err := j.Install(ctx, Image{}, Request{Targets: []string{fw + "slot_a"}}, func(int) {}); err != nil {
		t.Errorf("install into slot_a: %v", err)
	}
	if err := j.Activate(ctx, []string{fw + "slot_a"}, nil, func(int) {}); err != nil {
		t.Errorf("activate slot_a: %v", err)
	}
	if !slices.Equal(*calls, []string{"rauc validate", "rauc install", "rauc activate"}) {
		t.Errorf("calls = %v", *calls)
	}
	if err := j.Install(ctx, Image{}, Request{Targets: []string{fw + "p1"}}, func(int) {}); err == nil || err.Error() != "rsu_client missing" {
		t.Errorf("install into p1: err = %v, want the failing backend's error", err)
	}

	*calls = nil
	j = Join(failing{}, rauc)
	if inv, err := j.Inventory(ctx); err != nil || len(inv.Firmware) != 1 {
		t.Errorf("inventory = %v, %v; want slot_a", inv, err)
	}
	if err := j.Install(ctx, Image{}, Request{Targets: []string{fw + "slot_a"}}, func(int) {}); err == nil || err.Error() != "rsu_client missing" {
		t.Errorf("install into slot_a behind a failing backend: err = %v, want its error", err)
	}
	if j.CanActivate(ctx, fw+"slot_a") || len(*calls) != 0 {
		t.Errorf("slot_a behind a failing backend: CanActivate true or calls %v", *calls)
	}
	if _, err := Join(failing{}, failing{}).Inventory(ctx); err == nil {
		t.Error("inventory of failing backends did not fail")
	}
}

// TestJoinFirstOwner routes each target to the first backend that reports
// it, and refuses targets of different backends.
func TestJoinFirstOwner(t *testing.T) {
	calls := &[]string{}
	fw := FirmwareInventoryURI + "/"
	j := Join(
		&joinPart{name: "a", firmware: []string{"x"}, calls: calls},
		&joinPart{name: "b", firmware: []string{"x", "y"}, calls: calls},
	)
	ctx := context.Background()
	for _, tt := range []struct {
		targets []string
		want    []string
	}{
		{[]string{"x"}, []string{"a validate", "a install"}},
		{[]string{"y"}, []string{"b validate", "b install"}},
		{[]string{"x", "y"}, nil},
	} {
		*calls = nil
		var targets []string
		for _, id := range tt.targets {
			targets = append(targets, fw+id)
		}
		err := j.Install(ctx, Image{}, Request{Targets: targets}, func(int) {})
		if !slices.Equal(*calls, tt.want) || (tt.want == nil) != (err != nil) {
			t.Errorf("install into %v: err = %v, calls = %v; want %v", tt.targets, err, *calls, tt.want)
		}
	}
}

// TestJoinDuplicateIds checks that an Id two backends report belongs to the
// first, for listing as for updates.
func TestJoinDuplicateIds(t *testing.T) {
	calls := &[]string{}
	fw := FirmwareInventoryURI + "/"
	j := Join(
		&itemActivator{joinPart: joinPart{name: "first", firmware: []string{"x"}, calls: calls}},
		&joinPart{name: "second", firmware: []string{"x"}, calls: calls},
	)
	ctx := context.Background()
	if _, err := j.Inventory(ctx); err != nil {
		t.Fatal(err)
	}
	// The first backend refuses to activate x; the second would.
	if j.CanActivate(ctx, fw+"x") {
		t.Error("CanActivate follows the second backend")
	}
}
