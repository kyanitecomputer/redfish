# Roadmap

Implementation status of the Redfish service library. Status values: **done**, **partial**, **stub**, **planned**, **later**.

## Generator and runtime

| Item | Status | Notes |
|---|---|---|
| CSDL parsing and type resolution | done | DSP8010 2026.1 |
| Reflection-free models and marshalers | done | |
| Opt-in `Register*` per resource | done | |
| `$metadata` and OData service document | done | Trimmed to generated types |
| Collections and AutoExpand collections | done | |
| Sparse PATCH with `ETag`/`If-Match` | done | Scalar properties only |
| Bound actions with scalar and array parameters | done | Complex parameters skipped |
| Typed provider errors mapped to HTTP status and message | done | `server.Error` |
| Latest-version resolution of versioned complex property types | done | e.g. `Manager.Links.ActiveSoftwareImage` |
| Correct `@odata.id` and route identity for multi-URI resources | done | `server.Params.URI` |
| Action results: `200` body, `202 Accepted` with task monitor | done | `server.ActionResult` |
| Resources without `Redfish.Uris` (e.g. `ActionInfo`) | done | URIs supplied at registration |
| PATCH of complex and array properties | later | |
| `Allow` response header | later | |
| `$expand`, `$select`, other query parameters | later | |
| DSP8011 message registries | partial | Base and TaskEvent message IDs only; registries not served |
| Authentication and sessions | later | Required before exposing update endpoints |
| Generator unit tests | partial | |

## Firmware and software update

| Item | Status | Notes |
|---|---|---|
| `UpdateService` resource | done | `update` package |
| `FirmwareInventory` / `SoftwareInventory` collections and members | done | |
| `TaskService`, tasks, and task monitors | done | `task` package; bounded retention of finished tasks |
| Task cancellation (`DELETE` on a task monitor) | later | |
| `UpdateService.SimpleUpdate` (pull) | done | Image URI handed to the backend |
| `MultipartHttpPushUri` (push) | done | Streamed to the backend; optional `MaxImageSizeBytes` |
| `UpdateService.Activate`, `SoftwareInventory.Activate` | done | Optional `update.Activator` |
| Pluggable update backend interface | done | `update.Backend`, `Installer`, `Activator`; core is OS-independent |
| RAUC backend | stub | `update/rauc`; CLI-based implementation later |
| Intel/Altera RSU backend | stub | `update/rsu`; CLI-based implementation later |
| In-service image download (HTTP/HTTPS client) | later | Backends fetch `ImageURI` themselves for now |
| `SoftwareInventory.Cancel`, `UpdateService.StartUpdate` | later | |
| Validation of update `Targets` against the inventory | later | Left to the backend |
| `Update` message registry progress messages in tasks | later | |
| `LocalImageStore` | later | |
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
