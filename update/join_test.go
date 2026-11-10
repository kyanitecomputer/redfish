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
		}, 0, []string{"rauc install"}},
		{"install into the second backend", func(ctx context.Context, j *Joined) error {
			return j.Install(ctx, Image{}, Request{Targets: []string{fw + "p2"}}, func(int) {})
		}, 0, []string{"rsu install"}},
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
