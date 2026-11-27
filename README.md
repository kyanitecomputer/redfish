# Redfish

Redfish is an experimental, dependency-free Go library for serving a DMTF Redfish API, generated directly from the
normative CSDL schema and aimed at bare-metal BMC firmware.

Part of the [Kyanite](https://github.com/kyanitecomputer) stack.

> **Status:** experimental — expect breaking changes.

## Current capabilities

- Parses the DMTF DSP8010 CSDL (OData EDMX) schema bundle and resolves versioned entity, complex, enum, and
  type-definition references.
- Generates Go source for an explicit list of resources only, plus the complex and enum types they transitively need.
- Emits models with explicit marshalers and body parsers that use neither `reflect` nor `encoding/json`. (`net/http`
  itself links `reflect`, and the `update/rauc` backend decodes the output of `rauc status` with `encoding/json`.)
- Emits one opt-in `Register*` function per resource, so anything a binary does not register is removed by the linker.
- Uses `net/http.ServeMux` method+pattern routing; no third-party router.
- Covers the OData surface a Redfish service needs:
  - trimmed `$metadata` (EDMX) for the generated types and the OData service document
  - `@odata.id`, `@odata.type` and `@odata.context`, and `ETag` headers
  - collections (`Members`, `Members@odata.count`), including AutoExpand collections
  - standard Redfish error bodies
- Supports sparse `PATCH` on writable properties with `ETag`/`If-Match` checks, and bound actions such as
  `ComputerSystem.Reset` that complete synchronously or asynchronously as tasks.
- Lets providers return a `server.Error` to choose the HTTP status and base message of an error response.
- Authenticates requests with sessions (`X-Auth-Token`), HTTP Basic, or verified TLS client certificates, checks
  the privileges of the Administrator, Operator and ReadOnly roles, and serves the `AccountService` with accounts
  and roles and the `SessionService` with sessions. Passwords are stored as PBKDF2-HMAC-SHA256 hashes; failed logins
  lock the user name for a while, and at most two passwords, for logins or password changes, are hashed at a time.
  Changes sent by a browser from another origin are refused.
- Serves the `UpdateService` with firmware and software inventory, `SimpleUpdate`, multipart HTTP push updates,
  and `Activate` on top of a pluggable, OS-independent update backend.
- Ships an in-memory example BMC, which requires a login (admin/admin, over plain HTTP: for local use only), that passes the DMTF Redfish Service
  Validator, with Basic and with session authentication, and the Interop Validator against the OCP Baseline Hardware
  Management profile v1.1.1.

## Not yet complete

- `$expand`, `$select`, and other query parameters are not implemented.
- No `Allow` response header yet, so validators cannot test writable properties.
- Only the resources listed in `gen/doc.go` are generated; other resources need to be added there and regenerated.
- Only a subset of the DSP8011 base messages is used, and message registries are not served.
- The example uses static fixtures. The RAUC and Intel RSU update backends drive the `rauc` and `rsu_client`
  command line tools and have been tested on one board only (an Agilex 7 SoM).

## Packages

- `internal/csdl`: CSDL/EDMX model, parser, and schema bundle resolution.
- `internal/gen`: resource resolution, `$metadata` construction, and Go source rendering.
- `cmd/redfishgen`: generator command.
- `odata`: hand-written JSON builder and OData payload helpers used by generated code.
- `server`: response, ETag, and error helpers used by generated handlers, and `BodyIdleTimeout`, an inactivity
  limit for request bodies such as update uploads.
- `gen`: generated models, marshalers, and `Register*` functions for the baseline BMC resource set.
- `auth`: accounts, sessions, authentication and authorization middleware, and the account and session services.
  TLS is left to the `http.Server`.
- `task`: runs long-running operations as Redfish tasks and serves the TaskService and task monitors.
- `update`: serves the UpdateService and firmware/software inventory on top of an update backend, or of
  several joined with `update.Join`.
- `update/rauc`: update backend for Linux systems updated with RAUC, using the `rauc` command line tool.
- `update/rsu`: update backend for Intel (Altera) RSU, using `rsu_client` from LIBRSU and an installer
  command that reports like `rauc install`.
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

Design notes are in [docs/design.html](docs/design.html). Implementation status is tracked in
[ROADMAP.md](ROADMAP.md).

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
