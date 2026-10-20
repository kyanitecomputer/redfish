#!/usr/bin/env bash
#
# validate.sh boots the example BMC server and runs the DMTF Service Validator
# and the Interop Validator (OCP Baseline Hardware Management profile) against
# it. Exits non-zero if either validator reports a failure.
#
# Prerequisites (set up once, see the project notes):
#   - third_party/Redfish-Service-Validator and Redfish-Interop-Validator cloned
#   - a venv at third_party/.venv with both validators installed
#   - profiles/OCPBaselineHardwareManagement.v1_1_1.json present
#
# Usage: scripts/validate.sh
set -euo pipefail

cd "$(dirname "$0")/.."

PORT=8080
RHOST="http://127.0.0.1:${PORT}"
PY=third_party/.venv/bin/python
PROFILE=profiles/OCPBaselineHardwareManagement.v1_1_1.json
SVC=third_party/Redfish-Service-Validator/RedfishServiceValidator.py
INTEROP=third_party/Redfish-Interop-Validator/RedfishInteropValidator.py

export GOWORK=off
export GOEXPERIMENT=jsonv2

# Refuse to validate whatever else may be listening on the port.
if curl -fsS -o /dev/null "${RHOST}/redfish/v1" 2>/dev/null; then
	echo "port ${PORT} is already serving Redfish; stop that server first" >&2
	exit 1
fi

# Build and start the example server in the background. Running the binary
# directly (rather than via go run) ensures the trap stops the server itself.
BIN_DIR=$(mktemp -d)
go build -o "${BIN_DIR}/bmc" ./examples/bmc
"${BIN_DIR}/bmc" &
SERVER_PID=$!
trap 'kill "${SERVER_PID}" 2>/dev/null || true; rm -rf "${BIN_DIR}"' EXIT

# Wait for the service root to respond.
for _ in $(seq 1 30); do
	if curl -fsS -o /dev/null "${RHOST}/redfish/v1"; then
		break
	fi
	sleep 0.5
done

status=0

echo "== Redfish Service Validator =="
if ! "${PY}" "${SVC}" -r "${RHOST}" -u admin -p admin --authtype Basic \
	--schema_directory SchemaFiles --logdir logs; then
	echo "Service Validator: FAILED"
	status=1
fi

echo "== Redfish Interop Validator (OCP Baseline) =="
if ! "${PY}" "${INTEROP}" -r "${RHOST}" -u admin -p admin --authtype Basic \
	--logdir logs --no_online_profiles "${PROFILE}"; then
	echo "Interop Validator: FAILED"
	status=1
fi

if [ "${status}" -eq 0 ]; then
	echo "All validators passed."
else
	echo "One or more validators reported failures."
fi
exit "${status}"
