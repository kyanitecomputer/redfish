// Command bmc is a minimal example Redfish service built on the generated
// package. It serves a ServiceRoot, a Chassis collection and member, and a
// ComputerSystem collection and member backed by in-memory fixtures. Every
// request but the public ones needs a login: the account admin with the
// password admin, kept in memory.
//
// Only the resources registered here link into the binary; every other Redfish
// resource is eliminated by the linker.
package main

import (
	"log"
	"net/http"

	"src.kyanite.computer/redfish/auth"
	"src.kyanite.computer/redfish/gen"
	"src.kyanite.computer/redfish/task"
	"src.kyanite.computer/redfish/update"
)

func main() {
	h, err := newHandler(newBackend())
	if err != nil {
		log.Fatal(err)
	}
	log.Println("serving Redfish on :8080")
	log.Fatal(http.ListenAndServe(":8080", h))
}

// newHandler registers the served resources against a fresh ServeMux, behind
// the authentication of an in-memory admin account.
func newHandler(b *backend) (http.Handler, error) {
	accounts, err := auth.NewAccounts(&auth.Memory{})
	if err != nil {
		return nil, err
	}
	// The validators log in as admin/admin, shorter than the default minimum.
	p := accounts.Policy()
	p.MinPasswordLength = 5
	if err := accounts.SetPolicy(p); err != nil {
		return nil, err
	}
	if err := accounts.Create("admin", "admin", auth.Administrator); err != nil {
		return nil, err
	}
	authn := auth.New(accounts)
	mux := http.NewServeMux()
	authn.Register(mux)
	gen.RegisterMetadata(mux)
	gen.RegisterODataService(mux)
	gen.RegisterServiceRoot(mux, b)
	gen.RegisterChassisCollection(mux, b)
	gen.RegisterChassis(mux, b)
	gen.RegisterComputerSystemCollection(mux, b)
	gen.RegisterComputerSystem(mux, b)
	gen.RegisterResetComputerSystem(mux, b)
	gen.RegisterManagerCollection(mux, b)
	gen.RegisterManager(mux, b)
	gen.RegisterManagerNetworkProtocol(mux, b)
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
	tasks := task.New()
	tasks.Register(mux)
	update.New(b, tasks).Register(mux)
	return authn.Handler(mux), nil
}
