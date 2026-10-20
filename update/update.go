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
	"io"
	"net/http"
	"strings"
	"sync"

	"src.kyanite.computer/redfish/gen"
	"src.kyanite.computer/redfish/odata"
	"src.kyanite.computer/redfish/server"
	"src.kyanite.computer/redfish/task"
)

// URIs of the resources the service serves.
const (
	ServiceURI           = "/redfish/v1/UpdateService"
	FirmwareInventoryURI = ServiceURI + "/FirmwareInventory"
	SoftwareInventoryURI = ServiceURI + "/SoftwareInventory"
	SimpleUpdateURI      = ServiceURI + "/Actions/UpdateService.SimpleUpdate"
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

// Image is an update image. A SimpleUpdate request sets URI, and optionally
// credentials, leaving the transfer to the backend; a push update sets Body.
type Image struct {
	URI      string
	Username string
	Password string
	Body     io.Reader
}

// Request carries the parameters common to all update methods.
type Request struct {
	// Targets lists the @odata.id of the resources to update; when empty the
	// backend chooses the applicable targets.
	Targets []string
	// ExcludeTargets lists the @odata.id of resources not to update.
	ExcludeTargets []string
	// ForceUpdate requests the update even if the image is not newer.
	ForceUpdate bool
	// Stage requests the image to be staged but not activated.
	Stage bool
}

// Installer is implemented by backends that can install update images. The
// service only advertises the update actions when its backend implements it.
type Installer interface {
	// Install installs img and reports its progress in percent. It runs in a
	// task; returning a *server.Error chooses the error a client reads from the
	// task monitor. Only one Install runs at a time.
	Install(ctx context.Context, img Image, req Request, progress func(percent int)) error
}

// Service implements the generated UpdateService and SoftwareInventory
// providers on top of a Backend, running updates as tasks.
type Service struct {
	backend Backend
	tasks   *task.Manager

	mu      sync.Mutex
	enabled bool
	busy    bool
}

// New returns a Service backed by b that runs updates as tasks of tasks.
func New(b Backend, tasks *task.Manager) *Service {
	return &Service{backend: b, tasks: tasks, enabled: true}
}

// Register wires the UpdateService, the inventory collections and members, and
// the update actions the backend supports into mux.
func (s *Service) Register(mux *http.ServeMux) {
	gen.RegisterUpdateService(mux, s)
	gen.RegisterSoftwareInventoryCollection(mux, s)
	gen.RegisterSoftwareInventory(mux, s)
	if _, ok := s.backend.(Installer); ok {
		gen.RegisterSimpleUpdateUpdateService(mux, s)
	}
}

// GetUpdateService implements gen.UpdateServiceProvider.
func (s *Service) GetUpdateService(_ context.Context) (*gen.UpdateService, error) {
	s.mu.Lock()
	enabled := s.enabled
	s.mu.Unlock()
	us := &gen.UpdateService{
		Id:                "UpdateService",
		Name:              "Update Service",
		ServiceEnabled:    &enabled,
		FirmwareInventory: odata.Link(FirmwareInventoryURI),
		SoftwareInventory: odata.Link(SoftwareInventoryURI),
	}
	if _, ok := s.backend.(Installer); ok {
		us.Actions = &gen.UpdateServiceActions{
			SimpleUpdate: &odata.ActionTarget{Target: SimpleUpdateURI},
		}
	}
	return us, nil
}

// SimpleUpdateUpdateService implements gen.SimpleUpdateUpdateServiceProvider:
// it hands the image URI to the backend's Installer in a new task.
func (s *Service) SimpleUpdateUpdateService(_ context.Context, params *gen.UpdateServiceSimpleUpdateParameters) (*server.ActionResult, error) {
	const action = "UpdateService.SimpleUpdate"
	if params.ImageURI == nil || *params.ImageURI == "" {
		return nil, &server.Error{
			Status:    http.StatusBadRequest,
			MessageID: odata.MsgActionParameterMissing,
			Message:   "The action " + action + " requires the parameter ImageURI to be present in the request body.",
		}
	}
	if params.LocalImage != nil && *params.LocalImage {
		return nil, &server.Error{
			Status:    http.StatusBadRequest,
			MessageID: odata.MsgActionParameterNotSupported,
			Message:   "The parameter LocalImage for the action " + action + " is not supported on the target resource.",
		}
	}
	img := Image{URI: imageURI(*params.ImageURI, params.TransferProtocol)}
	if params.Username != nil {
		img.Username = *params.Username
	}
	if params.Password != nil {
		img.Password = *params.Password
	}
	req := Request{
		Targets:        params.Targets,
		ExcludeTargets: params.ExcludeTargets,
		ForceUpdate:    params.ForceUpdate != nil && *params.ForceUpdate,
		Stage:          params.Stage != nil && *params.Stage,
	}
	inst := s.backend.(Installer)
	return s.start("Simple Update", func(ctx context.Context, progress func(int)) error {
		return inst.Install(ctx, img, req, progress)
	})
}

// imageURI combines a SimpleUpdate ImageURI with the deprecated
// TransferProtocol parameter, which applies only to URIs without a scheme.
func imageURI(uri string, proto *gen.UpdateServiceTransferProtocolType) string {
	if proto == nil || *proto == "" || strings.Contains(uri, "://") {
		return uri
	}
	return strings.ToLower(string(*proto)) + "://" + uri
}

// start runs an update operation as a task, rejecting it while the service is
// disabled or another update is running.
func (s *Service) start(name string, fn task.Func) (*server.ActionResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled || s.busy {
		reason := "The update service is disabled."
		if s.busy {
			reason = "Another update is in progress."
		}
		return nil, &server.Error{
			Status:    http.StatusServiceUnavailable,
			MessageID: odata.MsgServiceUnavailable,
			Message:   "The service is temporarily unavailable. " + reason,
		}
	}
	s.busy = true
	return s.tasks.Start(name, func(ctx context.Context, progress func(int)) error {
		defer func() {
			s.mu.Lock()
			s.busy = false
			s.mu.Unlock()
		}()
		return fn(ctx, progress)
	}), nil
}

// PatchUpdateService implements gen.UpdateServiceProvider. Only ServiceEnabled
// is writable.
func (s *Service) PatchUpdateService(ctx context.Context, patch *gen.UpdateServicePatch) (*gen.UpdateService, error) {
	switch {
	case patch.HttpPushUriTargetsBusy != nil:
		return nil, server.NotWritable("HttpPushUriTargetsBusy")
	case patch.HttpPushUriOptionsBusy != nil:
		return nil, server.NotWritable("HttpPushUriOptionsBusy")
	case patch.VerifyRemoteServerCertificate != nil:
		return nil, server.NotWritable("VerifyRemoteServerCertificate")
	case patch.VerifyRemoteServerSSHKey != nil:
		return nil, server.NotWritable("VerifyRemoteServerSSHKey")
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
		return nil, server.NotWritable("WriteProtected")
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
