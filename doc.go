// Package redfish is the module root for a standalone, dependency-free
// DMTF Redfish HTTP server library generated from CSDL.
//
// The library is built around three layers:
//
//   - internal/csdl parses the DMTF DSP8010 CSDL (OData EDMX) schema bundle.
//   - cmd/redfishgen turns the parsed schema into reflection-free Go models,
//     explicit MarshalJSON marshalers, and one Register* function per resource.
//   - odata and server provide the small, hand-written runtime the generated
//     code links against.
//
// Only the Go standard library is used. Routing is performed by net/http's
// ServeMux (Go 1.22+ method+pattern matching); no third-party router or
// reflection-based framework is pulled in.
//
// Generated resources are opt-in: a Register* function is linked only when the
// consuming binary references it, so unimplemented resources are removed by the
// linker's dead-code elimination.
package redfish
