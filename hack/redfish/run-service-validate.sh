#!/usr/bin/env bash
# Runs the DMTF Redfish-Service-Validator against the Redfish API served by
# hack/redfish/interopserver (production read paths on in-memory fake clients).
# Mirrors the "Validate PR head" step of .github/workflows/redfish-service-validate.yml.
#
# Unlike the interop validator (profile conformance), this one validates every
# reachable resource against the CSDL schemas from the local DSP8010 bundle
# (make download-redfish-schema) -- the same schema version the generated API
# is built from -- so it needs no profile file and no live access to
# redfish.dmtf.org at validation time.
#
# Paths are resolved from the repo root. Requires rf_service_validator on PATH.
# Exit status is the validator's (non-zero while schema violations remain),
# unless EXIT_WITH_VALIDATOR=0 -- used by CI, which defers gating to the diff
# step and must not let a failed validation skip the base run. Infrastructure
# failures (build, readiness) always exit non-zero either way.
set -euo pipefail

cd "$(dirname "$0")/../.."

SCHEMA_DIR="hack/${REDFISH_SCHEMA_BUNDLE:-DSP8010_2023.3}/csdl"
BMC_USER=admin
BMC_PASSWORD=password
BMC_URL=http://127.0.0.1:8000
LOGDIR=${LOGDIR:-logs/local-service}
EXIT_WITH_VALIDATOR=${EXIT_WITH_VALIDATOR:-1}
VALIDATOR_VERSION=${VALIDATOR_VERSION:-3.1.7}

if ! command -v rf_service_validator >/dev/null; then
	echo "rf_service_validator not found; install with:" >&2
	echo "  pip install 'redfish_service_validator==${VALIDATOR_VERSION}'" >&2
	exit 1
fi

if [ ! -d "$SCHEMA_DIR" ]; then
	make download-redfish-schema
fi

# A globally configured proxy must not intercept the loopback requests.
export no_proxy="127.0.0.1,localhost${no_proxy:+,$no_proxy}"

tmpdir=$(mktemp -d)
server_pid=""
cleanup() {
	[ -n "$server_pid" ] && kill "$server_pid" 2>/dev/null || true
	rm -rf "$tmpdir"
}
trap cleanup EXIT

go build -o "$tmpdir/interopserver" ./hack/redfish/interopserver
"$tmpdir/interopserver" &
server_pid=$!

curl -sf --retry 30 --retry-delay 1 --retry-connrefused \
	-u "$BMC_USER:$BMC_PASSWORD" "$BMC_URL/redfish/v1/" >/dev/null

rm -rf "$LOGDIR"
mkdir -p "$LOGDIR"
status=0
rf_service_validator -i "$BMC_URL" -u "$BMC_USER" -p "$BMC_PASSWORD" \
	--authtype Basic --nooemcheck --schema_directory "$SCHEMA_DIR" \
	--logdir "$LOGDIR" 2>&1 | tee "$LOGDIR/stdout.txt" || status=$?

python3 hack/redfish/service-diff.py "$LOGDIR"
if [ "$EXIT_WITH_VALIDATOR" = "1" ]; then
	exit "$status"
fi
