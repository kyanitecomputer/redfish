// Package gen contains the generated Redfish resource models, marshalers, and
// Register functions for the resources this build serves. The contents are
// produced by cmd/redfishgen from the CSDL bundle under schema/csdl; do not edit
// redfish_gen.go by hand.
//
// To regenerate, adjust the resource list below and run `go generate ./gen`.
//
//go:generate go run ../cmd/redfishgen -schema ../schema/csdl -pkg gen -out redfish_gen.go ServiceRoot.ServiceRoot SessionService.SessionService SessionCollection.SessionCollection ChassisCollection.ChassisCollection Chassis.Chassis ComputerSystemCollection.ComputerSystemCollection ComputerSystem.ComputerSystem ManagerCollection.ManagerCollection Manager.Manager ManagerNetworkProtocol.ManagerNetworkProtocol AccountService.AccountService ManagerAccountCollection.ManagerAccountCollection ManagerAccount.ManagerAccount ThermalSubsystem.ThermalSubsystem PowerSubsystem.PowerSubsystem SensorCollection.SensorCollection ControlCollection.ControlCollection EnvironmentMetrics.EnvironmentMetrics EthernetInterfaceCollection.EthernetInterfaceCollection EthernetInterface.EthernetInterface LogServiceCollection.LogServiceCollection LogService.LogService LogEntryCollection.LogEntryCollection LogEntry.LogEntry
package gen
