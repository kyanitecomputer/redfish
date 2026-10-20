package update

import (
	"context"
	"net/http"
	"strings"

	"src.kyanite.computer/redfish/gen"
	"src.kyanite.computer/redfish/odata"
	"src.kyanite.computer/redfish/server"
)

// ActivateURI is the target of the UpdateService.Activate action.
const ActivateURI = ServiceURI + "/Actions/UpdateService.Activate"

// Activator is implemented by backends that can activate staged images, for
// example by switching the boot slot. The service only advertises the Activate
// actions when its backend implements it.
type Activator interface {
	// Activate activates the staged images, given as @odata.id of inventory
	// items, on devices, given as @odata.id of the devices; no devices means all
	// applicable ones. It runs in a task like Installer.Install, and never
	// concurrently with it.
	Activate(ctx context.Context, images, devices []string, progress func(percent int)) error
}

// ActivateUpdateService implements gen.ActivateUpdateServiceProvider: it
// activates the staged images listed in Targets.
func (s *Service) ActivateUpdateService(_ context.Context, params *gen.UpdateServiceActivateParameters) (*server.ActionResult, error) {
	if len(params.Targets) == 0 {
		return nil, &server.Error{
			Status:    http.StatusBadRequest,
			MessageID: odata.MsgActionParameterMissing,
			Message:   "The action UpdateService.Activate requires the parameter Targets to be present in the request body.",
		}
	}
	return s.activate(odata.Strings(params.Targets), nil)
}

// ActivateSoftwareInventory implements gen.ActivateSoftwareInventoryProvider:
// it activates the staged image of one inventory item on the devices listed in
// Targets.
func (s *Service) ActivateSoftwareInventory(ctx context.Context, p server.Params, params *gen.SoftwareInventoryActivateParameters) (*server.ActionResult, error) {
	image := strings.TrimSuffix(p.Path(), "/Actions/SoftwareInventory.Activate")
	item, err := s.GetSoftwareInventory(ctx, p)
	if err != nil {
		return nil, err
	}
	if item == nil {
		return nil, &server.Error{
			Status:    http.StatusNotFound,
			MessageID: odata.MsgResourceNotFound,
			Message:   "The resource at URI '" + image + "' was not found.",
		}
	}
	return s.activate([]string{image}, odata.Strings(params.Targets))
}

func (s *Service) activate(images, devices []string) (*server.ActionResult, error) {
	act := s.backend.(Activator)
	return s.start("Activate", func(ctx context.Context, progress func(int)) error {
		return act.Activate(ctx, images, devices, progress)
	})
}
