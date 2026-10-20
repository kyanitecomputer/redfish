# Roadmap

Implementation status of the Redfish service library. Status values: **done**, **partial**, **planned**, **later**.

## Generator and runtime

| Item | Status | Notes |
|---|---|---|
| CSDL parsing and type resolution | done | DSP8010 2026.1 |
| Reflection-free models and marshalers | done | |
| Opt-in `Register*` per resource | done | |
| `$metadata` and OData service document | done | Trimmed to generated types |
| Collections and AutoExpand collections | done | |
| Sparse PATCH with `ETag`/`If-Match` | done | Scalar properties only |
| Bound actions with scalar parameters | done | |
| Typed provider errors mapped to HTTP status and message | planned | |
| Latest-version resolution of versioned complex property types | planned | e.g. `Manager.Links.ActiveSoftwareImage` |
| Array action parameters (strings, references) | planned | `SimpleUpdate.Targets`, `Activate.Targets` |
| Correct `@odata.id` and route identity for multi-URI resources | planned | `FirmwareInventory` vs `SoftwareInventory` |
| Action results: `200` body, `202 Accepted` with task monitor | planned | |
| Resources without `Redfish.Uris` (e.g. `ActionInfo`) | planned | URIs supplied at registration |
| PATCH of complex and array properties | later | |
| `Allow` response header | later | |
| `$expand`, `$select`, other query parameters | later | |
| DSP8011 message registries | later | Base subset only |
| Authentication and sessions | later | Required before exposing update endpoints |
| Generator unit tests | partial | |

## Firmware and software update

| Item | Status | Notes |
|---|---|---|
| `UpdateService` resource | planned | |
| `FirmwareInventory` / `SoftwareInventory` collections and members | planned | |
| `TaskService`, tasks, and task monitors | planned | Asynchronous operations per DSP0266 |
| `UpdateService.SimpleUpdate` (pull) | planned | Image URI handed to the backend |
| `MultipartHttpPushUri` (push) | planned | Streamed to the backend |
| `UpdateService.Activate` / staged updates | planned | |
| Pluggable update backend interface | planned | Core stays OS-independent (TamaGo-compatible) |
| RAUC backend | planned | Stub only; CLI-based implementation later |
| Intel/Altera RSU backend | planned | Stub only; CLI-based implementation later |
| In-service image download (HTTP/HTTPS client) | later | |
| Remote server certificate handling | later | |
| `ApplyTime` / maintenance windows | later | |
| `ActionInfo` for update actions | later | |

## Hardware inventory

| Item | Status | Notes |
|---|---|---|
| Chassis, ComputerSystem, Manager | done | |
| Assembly | later | |
| Processor, Memory | later | |
| PCIeDevice, PCIeFunction | later | |
| Storage, Drive | later | |
| Power and thermal sensors | partial | Subsystems only, empty sensor collections |
