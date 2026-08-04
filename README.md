# Redfish

Redfish is an experimental, dependency-free Go library for serving a DMTF Redfish API, generated directly from the
normative CSDL schema and aimed at bare-metal BMC firmware.

Part of the [Kyanite](https://github.com/kyanitecomputer) stack.

> **Status:** experimental — expect breaking changes.

## Current capabilities

- Parses the DMTF DSP8010 CSDL (OData EDMX) schema bundle and resolves versioned entity, complex, enum, and
  type-definition references.
- Generates Go source for an explicit list of resources only, plus the complex and enum types they transitively need.
- Emits reflection-free models with explicit marshalers; no `reflect` or reflection-based `encoding/json` in the serving path.
- Emits one opt-in `Register*` function per resource, so anything a binary does not register is removed by the linker.
- Uses `net/http.ServeMux` method+pattern routing; no third-party router.
- Covers the OData surface a Redfish service needs:
  - trimmed `$metadata` (EDMX) for the generated types and the OData service document
  - `@odata.id`, `@odata.type`, `@odata.context`, and `@odata.etag` injection
  - collections (`Members`, `Members@odata.count`), including AutoExpand collections
  - standard Redfish error bodies
- Supports sparse `PATCH` on writable properties with `ETag`/`If-Match` checks, and bound actions such as
  `ComputerSystem.Reset`.
- Ships an in-memory example BMC that passes the DMTF Redfish Service Validator and the Interop Validator against the
  OCP Baseline Hardware Management profile v1.1.1.

## Not yet complete

- No authentication or session handling; `SessionService` is modelled but sessions are not enforced.
- `$expand`, `$select`, and other query parameters are not implemented.
- No `Allow` response header yet, so validators cannot test writable properties.
- Only the resources listed in `gen/doc.go` are generated; other resources need to be added there and regenerated.
- Backend errors are not yet mapped to the DSP8011 base message registry.
- No hardware-backed provider implementations; the example uses static fixtures.

## Packages

- `internal/csdl`: CSDL/EDMX model, parser, and schema bundle resolution.
- `internal/gen`: resource resolution, `$metadata` construction, and Go source rendering.
- `cmd/redfishgen`: generator command.
- `odata`: hand-written JSON builder and OData payload helpers used by generated code.
- `server`: response, ETag, and error helpers used by generated handlers.
- `gen`: generated models, marshalers, and `Register*` functions for the baseline BMC resource set.
- `examples/bmc`: minimal in-memory Redfish service built on `gen`.
- `schema/csdl`: vendored CSDL from DMTF DSP8010 (version in `schema/VERSION`).

## Development

PATCH and action bodies are parsed with `encoding/json/jsontext`, which currently needs the JSON v2 experiment:

```sh
export GOEXPERIMENT=jsonv2
go build ./...
go test ./...
```

Regenerate the resource package after changing the target list in `gen/doc.go`:

```sh
go generate ./gen
```

Run the example service on `:8080`:

```sh
go run ./examples/bmc
```

`scripts/validate.sh` boots the example and runs the DMTF
[Redfish-Service-Validator](https://github.com/DMTF/Redfish-Service-Validator) and
[Redfish-Interop-Validator](https://github.com/DMTF/Redfish-Interop-Validator). It expects both validators cloned under
`third_party/` with a Python venv at `third_party/.venv`, the extracted DSP8010 schema files in `SchemaFiles/`, and the
OCP profile at `profiles/OCPBaselineHardwareManagement.v1_1_1.json`.

Design notes are in [docs/design.html](docs/design.html).

## Contributing

See the org-wide [CONTRIBUTING guide](https://github.com/kyanitecomputer/.github/blob/main/CONTRIBUTING.md).
Contributions are dual-licensed.

## Security

See the org-wide [SECURITY policy](https://github.com/kyanitecomputer/.github/blob/main/SECURITY.md).

## License

Dual-licensed under either of Apache-2.0 ([LICENSE-APACHE](LICENSE-APACHE)) or
MIT ([LICENSE-MIT](LICENSE-MIT)) at your option.

The CSDL schema files under `schema/csdl` are copyright DMTF and distributed under the
[DMTF copyright policy](https://www.dmtf.org/about/policies/copyright).
