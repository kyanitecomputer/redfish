package main

import (
	"context"

	"src.kyanite.computer/redfish/gen"
	"src.kyanite.computer/redfish/odata"
	"src.kyanite.computer/redfish/server"
	"src.kyanite.computer/redfish/update"
)

// backend is an in-memory implementation of the generated provider interfaces.
// A real BMC would read these values from hardware (PMBus, I2C, GPIO, flash).
type backend struct {
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
	indicator := true
	return &gen.Chassis{
		Id:                      "1",
		Name:                    "Main Chassis",
		ChassisType:             gen.ChassisChassisTypeRackMount,
		Manufacturer:            "Kyanite",
		Model:                   "Cairn-1U",
		SerialNumber:            "CRN-0001",
		IndicatorLED:            gen.ChassisIndicatorLEDOff,
		LocationIndicatorActive: &indicator,
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
		SessionService: odata.Link("/redfish/v1/SessionService"),
		AccountService: odata.Link("/redfish/v1/AccountService"),
		UpdateService:  odata.Link(update.ServiceURI),
		Links:          gen.ServiceRootLinks{Sessions: odata.Link("/redfish/v1/SessionService/Sessions")},
	}, nil
}

func (b *backend) GetSessionService(_ context.Context) (*gen.SessionService, error) {
	enabled := true
	timeout := int64(600)
	return &gen.SessionService{
		Id:             "SessionService",
		Name:           "Session Service",
		ServiceEnabled: &enabled,
		SessionTimeout: &timeout,
		Sessions:       odata.Link("/redfish/v1/SessionService/Sessions"),
	}, nil
}

func (b *backend) ListSession(_ context.Context) ([]string, error) {
	return []string{}, nil // no active sessions in this example
}

func (b *backend) PatchSessionService(ctx context.Context, patch *gen.SessionServicePatch) (*gen.SessionService, error) {
	ss, err := b.GetSessionService(ctx)
	if err != nil {
		return nil, err
	}
	if patch.ServiceEnabled != nil {
		ss.ServiceEnabled = patch.ServiceEnabled
	}
	if patch.SessionTimeout != nil {
		ss.SessionTimeout = patch.SessionTimeout
	}
	return ss, nil
}

func (b *backend) PatchChassis(_ context.Context, p server.Params, patch *gen.ChassisPatch) (*gen.Chassis, error) {
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
	return c, nil
}

func (b *backend) PatchComputerSystem(_ context.Context, p server.Params, patch *gen.ComputerSystemPatch) (*gen.ComputerSystem, error) {
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
	return s, nil
}

func (b *backend) ListChassis(_ context.Context) ([]string, error) {
	return []string{"/redfish/v1/Chassis/1"}, nil
}

func (b *backend) GetChassis(_ context.Context, p server.Params) (*gen.Chassis, error) {
	return b.chassis[p.Get("ChassisId")], nil // nil maps to a 404 in the generated handler
}

func (b *backend) ListComputerSystem(_ context.Context) ([]string, error) {
	return []string{"/redfish/v1/Systems/1"}, nil
}

func (b *backend) GetComputerSystem(_ context.Context, p server.Params) (*gen.ComputerSystem, error) {
	return b.systems[p.Get("ComputerSystemId")], nil
}

// ResetComputerSystem performs the #ComputerSystem.Reset action, updating the
// in-memory PowerState. A real BMC would drive the platform reset lines.
func (b *backend) ResetComputerSystem(_ context.Context, p server.Params, params *gen.ComputerSystemResetParameters) (*server.ActionResult, error) {
	s := b.systems[p.Get("ComputerSystemId")]
	if s == nil || params.ResetType == nil {
		return nil, nil
	}
	switch *params.ResetType {
	case gen.ResourceResetTypeForceOff, gen.ResourceResetTypeGracefulShutdown:
		s.PowerState = gen.ResourcePowerStateOff
	default:
		s.PowerState = gen.ResourcePowerStateOn
	}
	return nil, nil
}
