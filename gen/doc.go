// Package gen contains the generated Redfish resource models, marshalers, and
// Register functions for the resources this build serves. The contents are
// produced by cmd/redfishgen from the CSDL bundle under schema/csdl; do not edit
// redfish_gen.go by hand.
//
// To regenerate, adjust the resource list in targets.txt and run
// `go generate ./gen`.
//
//go:generate go run ../cmd/redfishgen -schema ../schema/csdl -pkg gen -out redfish_gen.go -targets targets.txt
package gen
