// Command bmc is a minimal example Redfish service built on the generated
// package. It serves a ServiceRoot, a Chassis collection and member, and a
// ComputerSystem collection and member backed by in-memory fixtures.
//
// Only the resources registered here link into the binary; every other Redfish
// resource is eliminated by the linker.
package main

import (
	"log"
	"net/http"

	"src.kyanite.computer/redfish/gen"
)

func main() {
	mux := newMux(newBackend())
	log.Println("serving Redfish on :8080")
	log.Fatal(http.ListenAndServe(":8080", mux))
}

// newMux registers the served resources against a fresh ServeMux.
func newMux(b *backend) *http.ServeMux {
	mux := http.NewServeMux()
	gen.RegisterMetadata(mux)
	gen.RegisterODataService(mux)
	gen.RegisterServiceRoot(mux, b)
	gen.RegisterSessionService(mux, b)
	gen.RegisterSessionCollection(mux, b)
	gen.RegisterChassisCollection(mux, b)
	gen.RegisterChassis(mux, b)
	gen.RegisterComputerSystemCollection(mux, b)
	gen.RegisterComputerSystem(mux, b)
	gen.RegisterResetComputerSystem(mux, b)
	gen.RegisterManagerCollection(mux, b)
	gen.RegisterManager(mux, b)
	gen.RegisterManagerNetworkProtocol(mux, b)
	gen.RegisterAccountService(mux, b)
	gen.RegisterManagerAccountCollection(mux, b)
	gen.RegisterManagerAccount(mux, b)
	gen.RegisterThermalSubsystem(mux, b)
	gen.RegisterPowerSubsystem(mux, b)
	gen.RegisterSensorCollection(mux, b)
	gen.RegisterControlCollection(mux, b)
	gen.RegisterEnvironmentMetrics(mux, b)
	gen.RegisterEthernetInterfaceCollection(mux, b)
	gen.RegisterEthernetInterface(mux, b)
	gen.RegisterLogServiceCollection(mux, b)
	gen.RegisterLogService(mux, b)
	gen.RegisterLogEntryCollection(mux, b)
	gen.RegisterLogEntry(mux, b)
	return mux
}
