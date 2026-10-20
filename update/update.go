// Package update serves the Redfish UpdateService and the firmware and software
// inventory on top of a pluggable Backend.
//
// The package is independent of the operating system: everything that touches
// flash, boot slots, or update tooling lives in the Backend, so the same service
// runs on Linux (for example with a RAUC or Intel RSU backend) and on bare-metal
// TamaGo.
package update

import (
	"context"
	"net/http"
	"strings"
	"sync"

	"src.kyanite.computer/redfish/gen"
	"src.kyanite.computer/redfish/odata"
	"src.kyanite.computer/redfish/server"
)

// URIs of the resources the service serves.
const (
	ServiceURI           = "/redfish/v1/UpdateService"
	FirmwareInventoryURI = ServiceURI + "/FirmwareInventory"
	SoftwareInventoryURI = ServiceURI + "/SoftwareInventory"
)

// Inventory lists the components a backend reports. Each entry needs an Id
// unique within its list; the service derives @odata.id from it.
type Inventory struct {
	Firmware []*gen.SoftwareInventory
	Software []*gen.SoftwareInventory
}

// Backend provides the platform-specific side of the update service.
type Backend interface {
	// Inventory returns the current firmware and software inventory.
	Inventory(ctx context.Context) (*Inventory, error)
}

// Service implements the generated UpdateService and SoftwareInventory
// providers on top of a Backend.
type Service struct {
	backend Backend

	mu      sync.Mutex
	enabled bool
}

// New returns a Service backed by b.
func New(b Backend) *Service {
	return &Service{backend: b, enabled: true}
}

// Register wires the UpdateService and the inventory collections and members
// into mux.
func (s *Service) Register(mux *http.ServeMux) {
	gen.RegisterUpdateService(mux, s)
	gen.RegisterSoftwareInventoryCollection(mux, s)
	gen.RegisterSoftwareInventory(mux, s)
}

// GetUpdateService implements gen.UpdateServiceProvider.
func (s *Service) GetUpdateService(_ context.Context) (*gen.UpdateService, error) {
	s.mu.Lock()
	enabled := s.enabled
	s.mu.Unlock()
	return &gen.UpdateService{
		Id:                "UpdateService",
		Name:              "Update Service",
		ServiceEnabled:    &enabled,
		FirmwareInventory: odata.Link(FirmwareInventoryURI),
		SoftwareInventory: odata.Link(SoftwareInventoryURI),
	}, nil
}

// PatchUpdateService implements gen.UpdateServiceProvider. Only ServiceEnabled
// is writable.
func (s *Service) PatchUpdateService(ctx context.Context, patch *gen.UpdateServicePatch) (*gen.UpdateService, error) {
	switch {
	case patch.HttpPushUriTargetsBusy != nil:
		return nil, notWritable("HttpPushUriTargetsBusy")
	case patch.HttpPushUriOptionsBusy != nil:
		return nil, notWritable("HttpPushUriOptionsBusy")
	case patch.VerifyRemoteServerCertificate != nil:
		return nil, notWritable("VerifyRemoteServerCertificate")
	case patch.VerifyRemoteServerSSHKey != nil:
		return nil, notWritable("VerifyRemoteServerSSHKey")
	}
	if patch.ServiceEnabled != nil {
		s.mu.Lock()
		s.enabled = *patch.ServiceEnabled
		s.mu.Unlock()
	}
	return s.GetUpdateService(ctx)
}

// ListSoftwareInventory implements gen.SoftwareInventoryCollectionProvider for
// both the firmware and the software inventory.
func (s *Service) ListSoftwareInventory(ctx context.Context, p server.Params) ([]string, error) {
	items, base, err := s.inventory(ctx, p)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(items))
	for i, it := range items {
		ids[i] = base + "/" + it.Id
	}
	return ids, nil
}

// GetSoftwareInventory implements gen.SoftwareInventoryProvider.
func (s *Service) GetSoftwareInventory(ctx context.Context, p server.Params) (*gen.SoftwareInventory, error) {
	items, _, err := s.inventory(ctx, p)
	if err != nil {
		return nil, err
	}
	id := p.Get("SoftwareInventoryId")
	for _, it := range items {
		if it.Id == id {
			c := *it
			c.ODataID = p.Path()
			return &c, nil
		}
	}
	return nil, nil
}

// PatchSoftwareInventory implements gen.SoftwareInventoryProvider. No
// inventory property is writable.
func (s *Service) PatchSoftwareInventory(ctx context.Context, p server.Params, patch *gen.SoftwareInventoryPatch) (*gen.SoftwareInventory, error) {
	if patch.WriteProtected != nil {
		return nil, notWritable("WriteProtected")
	}
	return s.GetSoftwareInventory(ctx, p)
}

// inventory returns the inventory list the matched route belongs to and the URI
// of its collection.
func (s *Service) inventory(ctx context.Context, p server.Params) ([]*gen.SoftwareInventory, string, error) {
	var base string
	switch {
	case strings.HasPrefix(p.URI, FirmwareInventoryURI):
		base = FirmwareInventoryURI
	case strings.HasPrefix(p.URI, SoftwareInventoryURI):
		base = SoftwareInventoryURI
	default:
		return nil, "", &server.Error{
			Status:    http.StatusNotFound,
			MessageID: odata.MsgResourceNotFound,
			Message:   "The resource at URI '" + p.Path() + "' was not found.",
		}
	}
	inv, err := s.backend.Inventory(ctx)
	if err != nil {
		return nil, "", err
	}
	if inv == nil {
		return nil, base, nil
	}
	if base == FirmwareInventoryURI {
		return inv.Firmware, base, nil
	}
	return inv.Software, base, nil
}

func notWritable(property string) error {
	return &server.Error{
		Status:    http.StatusBadRequest,
		MessageID: odata.MsgPropertyNotWrite,
		Message:   "The property " + property + " is a read-only property and cannot be assigned a value.",
	}
}
