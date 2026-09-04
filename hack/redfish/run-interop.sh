#!/usr/bin/env bash
# Runs the DMTF Redfish-Interop-Validator against the Redfish API served by
# hack/redfish/interopserver (production read paths on in-memory fake clients).
# Mirrors the "Validate PR head" step of .github/workflows/redfish-interop.yml.
#
# Paths are resolved from the repo root. Requires rf_interop_validator on PATH.
# Exit status is the validator's (non-zero while profile gaps remain), unless
# EXIT_WITH_VALIDATOR=0 — used by CI, which defers gating to the diff step and
# must not let a failed validation skip the base run. Infrastructure failures
# (build, readiness) always exit non-zero either way.
set -euo pipefail

cd "$(dirname "$0")/../.."

PROFILE=${PROFILE:-hack/redfish/profiles/OpenStackIronicProfile.v1_2_0.json}
VALIDATOR_VERSION=${VALIDATOR_VERSION:-2.3.6}
BMC_USER=admin
BMC_PASSWORD=password
BMC_URL=http://127.0.0.1:8000
LOGDIR=${LOGDIR:-logs/local}
EXIT_WITH_VALIDATOR=${EXIT_WITH_VALIDATOR:-1}

if ! command -v rf_interop_validator >/dev/null; then
	echo "rf_interop_validator not found; install with:" >&2
	echo "  pip install 'redfish_interop_validator==${VALIDATOR_VERSION}'" >&2
	exit 1
fi

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

mkdir -p "$LOGDIR"
status=0
rf_interop_validator -i "$BMC_URL" -u "$BMC_USER" -p "$BMC_PASSWORD" \
	--authtype Basic --forceauth --no_online_profiles \
	--logdir "$LOGDIR" "$PROFILE" 2>&1 | tee "$LOGDIR/stdout.txt" || status=$?

python3 hack/redfish/interop-diff.py "$LOGDIR"
if [ "$EXIT_WITH_VALIDATOR" = "1" ]; then
	exit "$status"
fi
