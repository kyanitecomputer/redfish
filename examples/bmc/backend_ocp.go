package main

import (
	"context"

	"src.kyanite.computer/redfish/gen"
	"src.kyanite.computer/redfish/odata"
	"src.kyanite.computer/redfish/server"
)

// This file implements the additional resources required by the OCP Baseline
// Hardware Management profile: Managers, AccountService/Accounts, and the
// Chassis Thermal/Power subsystems. Data is static; a real BMC would source it
// from hardware and persistent configuration.
//
// Parameterized resources receive a server.Params carrying the path segments of
// the matched route (for example "ManagerId").

// --- Managers ---------------------------------------------------------------

func (b *backend) ListManager(_ context.Context) ([]string, error) {
	return []string{"/redfish/v1/Managers/bmc"}, nil
}

func (b *backend) GetManager(_ context.Context, p server.Params) (*gen.Manager, error) {
	if p.Get("ManagerId") != "bmc" {
		return nil, nil
	}
	return &gen.Manager{
		Id:                 "bmc",
		Name:               "Manager",
		ManagerType:        gen.ManagerManagerTypeBMC,
		UUID:               "92384634-2938-2342-8820-489239905431",
		FirmwareVersion:    "1.0.0",
		Status:             &gen.ResourceStatus{State: gen.ResourceStateEnabled, Health: gen.ResourceHealthOK},
		NetworkProtocol:    odata.Link("/redfish/v1/Managers/bmc/NetworkProtocol"),
		EthernetInterfaces: odata.Link("/redfish/v1/Managers/bmc/EthernetInterfaces"),
		LogServices:        odata.Link("/redfish/v1/Managers/bmc/LogServices"),
		Links:              &gen.ManagerLinks{},
	}, nil
}

func (b *backend) PatchManager(ctx context.Context, p server.Params, _ *gen.ManagerPatch) (*gen.Manager, error) {
	return b.GetManager(ctx, p)
}

func (b *backend) GetManagerNetworkProtocol(_ context.Context, p server.Params) (*gen.ManagerNetworkProtocol, error) {
	if p.Get("ManagerId") != "bmc" {
		return nil, nil
	}
	httpsEnabled := true
	return &gen.ManagerNetworkProtocol{
		Id:       "NetworkProtocol",
		Name:     "Manager Network Protocol",
		HostName: "cairn-bmc",
		FQDN:     "cairn-bmc.local",
		Status:   &gen.ResourceStatus{State: gen.ResourceStateEnabled, Health: gen.ResourceHealthOK},
		HTTPS:    &gen.ManagerNetworkProtocolHTTPSProtocol{ProtocolEnabled: &httpsEnabled, Port: i64(443)},
	}, nil
}

func (b *backend) PatchManagerNetworkProtocol(ctx context.Context, p server.Params, _ *gen.ManagerNetworkProtocolPatch) (*gen.ManagerNetworkProtocol, error) {
	return b.GetManagerNetworkProtocol(ctx, p)
}

// --- AccountService ---------------------------------------------------------

func (b *backend) GetAccountService(_ context.Context, _ server.Params) (*gen.AccountService, error) {
	enabled := true
	return &gen.AccountService{
		Id:             "AccountService",
		Name:           "Account Service",
		ServiceEnabled: &enabled,
		Status:         &gen.ResourceStatus{State: gen.ResourceStateEnabled, Health: gen.ResourceHealthOK},
		Accounts:       odata.Link("/redfish/v1/AccountService/Accounts"),
	}, nil
}

func (b *backend) PatchAccountService(ctx context.Context, p server.Params, _ *gen.AccountServicePatch) (*gen.AccountService, error) {
	return b.GetAccountService(ctx, p)
}

func (b *backend) ListManagerAccount(_ context.Context, _ server.Params) ([]string, error) {
	return []string{"/redfish/v1/AccountService/Accounts/admin"}, nil
}

func (b *backend) GetManagerAccount(_ context.Context, p server.Params) (*gen.ManagerAccount, error) {
	if p.Get("ManagerAccountId") != "admin" {
		return nil, nil
	}
	enabled := true
	return &gen.ManagerAccount{
		Id:           "admin",
		Name:         "Admin Account",
		UserName:     "admin",
		RoleId:       "Administrator",
		Enabled:      &enabled,
		AccountTypes: []string{"Redfish"},
	}, nil
}

func (b *backend) PatchManagerAccount(ctx context.Context, p server.Params, _ *gen.ManagerAccountPatch) (*gen.ManagerAccount, error) {
	return b.GetManagerAccount(ctx, p)
}

// --- Chassis subsystems, sub-collections, and metrics -----------------------

func (b *backend) GetThermalSubsystem(_ context.Context, p server.Params) (*gen.ThermalSubsystem, error) {
	if b.chassis[p.Get("ChassisId")] == nil {
		return nil, nil
	}
	return &gen.ThermalSubsystem{
		Id:     "ThermalSubsystem",
		Name:   "Thermal Subsystem",
		Status: &gen.ResourceStatus{State: gen.ResourceStateEnabled, Health: gen.ResourceHealthOK},
	}, nil
}

func (b *backend) PatchThermalSubsystem(ctx context.Context, p server.Params, _ *gen.ThermalSubsystemPatch) (*gen.ThermalSubsystem, error) {
	return b.GetThermalSubsystem(ctx, p)
}

func (b *backend) GetPowerSubsystem(_ context.Context, p server.Params) (*gen.PowerSubsystem, error) {
	if b.chassis[p.Get("ChassisId")] == nil {
		return nil, nil
	}
	return &gen.PowerSubsystem{
		Id:     "PowerSubsystem",
		Name:   "Power Subsystem",
		Status: &gen.ResourceStatus{State: gen.ResourceStateEnabled, Health: gen.ResourceHealthOK},
	}, nil
}

func (b *backend) ListSensor(_ context.Context, _ server.Params) ([]string, error) {
	return []string{}, nil
}
func (b *backend) ListControl(_ context.Context, _ server.Params) ([]string, error) {
	return []string{}, nil
}

func (b *backend) GetEnvironmentMetrics(_ context.Context, _ server.Params) (*gen.EnvironmentMetrics, error) {
	return &gen.EnvironmentMetrics{Id: "EnvironmentMetrics", Name: "Environment Metrics"}, nil
}

// --- Manager sub-resources --------------------------------------------------

func (b *backend) ListEthernetInterface(_ context.Context, p server.Params) ([]string, error) {
	if p.Get("ManagerId") != "bmc" {
		return []string{}, nil
	}
	return []string{"/redfish/v1/Managers/bmc/EthernetInterfaces/eth0"}, nil
}

func (b *backend) GetEthernetInterface(_ context.Context, p server.Params) (*gen.EthernetInterface, error) {
	if p.Get("ManagerId") != "bmc" || p.Get("EthernetInterfaceId") != "eth0" {
		return nil, nil
	}
	enabled := true
	return &gen.EthernetInterface{
		Id:               "eth0",
		Name:             "Manager Ethernet Interface",
		MACAddress:       "AA:BB:CC:DD:EE:00",
		SpeedMbps:        i64(1000),
		InterfaceEnabled: &enabled,
		LinkStatus:       gen.EthernetInterfaceLinkStatusLinkUp,
		Status:           &gen.ResourceStatus{State: gen.ResourceStateEnabled, Health: gen.ResourceHealthOK},
		HostName:         "cairn-bmc",
		FQDN:             "cairn-bmc.local",
		NameServers:      []string{"192.0.2.53"},
		IPv4Addresses: []gen.IPAddressesIPv4Address{{
			Address:       "192.0.2.10",
			SubnetMask:    "255.255.255.0",
			Gateway:       "192.0.2.1",
			AddressOrigin: gen.IPAddressesIPv4AddressOriginStatic,
		}},
	}, nil
}

func (b *backend) PatchEthernetInterface(ctx context.Context, p server.Params, _ *gen.EthernetInterfacePatch) (*gen.EthernetInterface, error) {
	return b.GetEthernetInterface(ctx, p)
}

func (b *backend) ListLogService(_ context.Context, p server.Params) ([]string, error) {
	if p.Get("ManagerId") != "bmc" {
		return []string{}, nil
	}
	return []string{"/redfish/v1/Managers/bmc/LogServices/Log"}, nil
}

func (b *backend) GetLogService(_ context.Context, p server.Params) (*gen.LogService, error) {
	if p.Get("ManagerId") != "bmc" || p.Get("LogServiceId") != "Log" {
		return nil, nil
	}
	enabled := true
	return &gen.LogService{
		Id:             "Log",
		Name:           "Manager Log Service",
		ServiceEnabled: &enabled,
		Entries:        odata.Link("/redfish/v1/Managers/bmc/LogServices/Log/Entries"),
	}, nil
}

func (b *backend) PatchLogService(ctx context.Context, p server.Params, _ *gen.LogServicePatch) (*gen.LogService, error) {
	return b.GetLogService(ctx, p)
}

func (b *backend) ListLogEntry(ctx context.Context, p server.Params) ([]*gen.LogEntry, error) {
	if p.Get("ManagerId") != "bmc" || p.Get("LogServiceId") != "Log" {
		return nil, nil
	}
	// AutoExpand collection: return fully-populated members with ODataID set.
	e := b.logEntry("1")
	e.ODataID = "/redfish/v1/Managers/bmc/LogServices/Log/Entries/1"
	return []*gen.LogEntry{e}, nil
}

func (b *backend) GetLogEntry(_ context.Context, p server.Params) (*gen.LogEntry, error) {
	if p.Get("ManagerId") != "bmc" || p.Get("LogServiceId") != "Log" || p.Get("LogEntryId") != "1" {
		return nil, nil
	}
	return b.logEntry("1"), nil
}

func (b *backend) PatchLogEntry(ctx context.Context, p server.Params, _ *gen.LogEntryPatch) (*gen.LogEntry, error) {
	return b.GetLogEntry(ctx, p)
}

func (b *backend) logEntry(id string) *gen.LogEntry {
	return &gen.LogEntry{
		Id:        id,
		Name:      "Log Entry " + id,
		Created:   "2026-06-11T00:00:00Z",
		EntryType: gen.LogEntryLogEntryTypeEvent,
		Message:   "System started.",
		Severity:  gen.LogEntryEventSeverityOK,
	}
}

func i64(v int64) *int64 { return &v }
