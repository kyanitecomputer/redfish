package main

import (
	"context"
	"net/http"
	"sync"

	"src.kyanite.computer/redfish/auth"
	"src.kyanite.computer/redfish/gen"
	"src.kyanite.computer/redfish/odata"
	"src.kyanite.computer/redfish/server"
	"src.kyanite.computer/redfish/task"
	"src.kyanite.computer/redfish/update"
)

// backend is an in-memory implementation of the generated provider interfaces.
// A real BMC would read these values from hardware (PMBus, I2C, GPIO, flash).
// Requests run concurrently: mu guards the chassis and systems, and Get
// returns copies, as the generated handlers may set their ODataID.
type backend struct {
	mu      sync.Mutex
	chassis map[string]*gen.Chassis
	systems map[string]*gen.ComputerSystem
}

func newBackend() *backend {
	return &backend{
		chassis: map[string]*gen.Chassis{
			"1": chassisFixture(),
		},
		systems: map[string]*gen.ComputerSystem{
			"1": {
				Id:           "1",
				Name:         "Cairn System",
				Manufacturer: "Kyanite",
				PowerState:   gen.ResourcePowerStateOn,
				Actions: &gen.ComputerSystemActions{
					Reset: &odata.ActionTarget{
						Target: "/redfish/v1/Systems/1/Actions/ComputerSystem.Reset",
					},
				},
			},
		},
	}
}

// chassisFixture populates the Chassis properties the OCP Baseline Hardware
// Management profile requires. Every field used here is already present in the
// generated model; only the data is supplied by the backend.
func chassisFixture() *gen.Chassis {
	return &gen.Chassis{
		Id:                      "1",
		Name:                    "Main Chassis",
		ChassisType:             gen.ChassisTypeRackMount,
		Manufacturer:            "Kyanite",
		Model:                   "Cairn-1U",
		SerialNumber:            "CRN-0001",
		IndicatorLED:            gen.ChassisIndicatorLEDOff,
		LocationIndicatorActive: new(true),
		PowerState:              gen.ResourcePowerStateOn,
		Status:                  &gen.ResourceStatus{State: gen.ResourceStateEnabled, Health: gen.ResourceHealthOK},
		ThermalSubsystem:        odata.Link("/redfish/v1/Chassis/1/ThermalSubsystem"),
		PowerSubsystem:          odata.Link("/redfish/v1/Chassis/1/PowerSubsystem"),
		Sensors:                 odata.Link("/redfish/v1/Chassis/1/Sensors"),
		Controls:                odata.Link("/redfish/v1/Chassis/1/Controls"),
		EnvironmentMetrics:      odata.Link("/redfish/v1/Chassis/1/EnvironmentMetrics"),
		Links:                   &gen.ChassisLinks{ManagedBy: []odata.Link{"/redfish/v1/Managers/bmc"}},
	}
}

func (b *backend) GetServiceRoot(_ context.Context) (*gen.ServiceRoot, error) {
	return &gen.ServiceRoot{
		Id:             "RootService",
		Name:           "Root Service",
		RedfishVersion: "1.21.0",
		UUID:           "92384634-2938-2342-8820-489239905423",
		Systems:        odata.Link("/redfish/v1/Systems"),
		Chassis:        odata.Link("/redfish/v1/Chassis"),
		Managers:       odata.Link("/redfish/v1/Managers"),
		SessionService: odata.Link(auth.SessionServiceURI),
		AccountService: odata.Link(auth.AccountServiceURI),
		UpdateService:  odata.Link(update.ServiceURI),
		Tasks:          odata.Link(task.ServiceURI),
		Links:          gen.ServiceRootLinks{Sessions: odata.Link(auth.SessionsURI)},
	}, nil
}

func (b *backend) PatchChassis(_ context.Context, p server.Params, patch *gen.ChassisPatch) (*gen.Chassis, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	c := b.chassis[p.Get("ChassisId")]
	if c == nil {
		return nil, nil
	}
	if patch.AssetTag != nil {
		c.AssetTag = *patch.AssetTag
	}
	if patch.IndicatorLED != nil {
		c.IndicatorLED = *patch.IndicatorLED
	}
	if patch.LocationIndicatorActive != nil {
		c.LocationIndicatorActive = patch.LocationIndicatorActive
	}
	return new(*c), nil
}

func (b *backend) PatchComputerSystem(_ context.Context, p server.Params, patch *gen.ComputerSystemPatch) (*gen.ComputerSystem, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.systems[p.Get("ComputerSystemId")]
	if s == nil {
		return nil, nil
	}
	if patch.AssetTag != nil {
		s.AssetTag = *patch.AssetTag
	}
	if patch.HostName != nil {
		s.HostName = *patch.HostName
	}
	return new(*s), nil
}

func (b *backend) ListChassis(_ context.Context) ([]string, error) {
	return []string{"/redfish/v1/Chassis/1"}, nil
}

// GetChassis returns nil for an unknown chassis, which the generated handler
// answers with 404.
func (b *backend) GetChassis(_ context.Context, p server.Params) (*gen.Chassis, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if c := b.chassis[p.Get("ChassisId")]; c != nil {
		return new(*c), nil
	}
	return nil, nil
}

// hasChassis reports whether the chassis with the Id in p exists.
func (b *backend) hasChassis(p server.Params) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.chassis[p.Get("ChassisId")] != nil
}

func (b *backend) ListComputerSystem(_ context.Context) ([]string, error) {
	return []string{"/redfish/v1/Systems/1"}, nil
}

func (b *backend) GetComputerSystem(_ context.Context, p server.Params) (*gen.ComputerSystem, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if s := b.systems[p.Get("ComputerSystemId")]; s != nil {
		return new(*s), nil
	}
	return nil, nil
}

// ResetComputerSystem performs the #ComputerSystem.Reset action, updating the
// in-memory PowerState. Without a ResetType it power cycles the system. A
// real BMC would drive the platform reset lines.
func (b *backend) ResetComputerSystem(_ context.Context, p server.Params, params *gen.ComputerSystemResetParameters) (*server.ActionResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.systems[p.Get("ComputerSystemId")]
	if s == nil {
		return nil, &server.Error{
			Status:    http.StatusNotFound,
			MessageID: odata.MsgResourceMissingAtURI,
			Message:   "The resource at URI '" + p.Path() + "' was not found.",
		}
	}
	resetType := gen.ResourceResetTypePowerCycle
	if params.ResetType != nil {
		resetType = *params.ResetType
	}
	switch resetType {
	case gen.ResourceResetTypeForceOff, gen.ResourceResetTypeGracefulShutdown:
		s.PowerState = gen.ResourcePowerStateOff
	case gen.ResourceResetTypeOn, gen.ResourceResetTypeForceOn, gen.ResourceResetTypeForceRestart,
		gen.ResourceResetTypeGracefulRestart, gen.ResourceResetTypePowerCycle:
		s.PowerState = gen.ResourcePowerStateOn
	default:
		return nil, &server.Error{
			Status:    http.StatusBadRequest,
			MessageID: odata.MsgActionParameterValueFormatError,
			Message:   "The value " + string(resetType) + " for the parameter ResetType in the action ComputerSystem.Reset is not supported.",
		}
	}
	return nil, nil
}
