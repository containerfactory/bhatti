#!/usr/bin/env bash
#
# Run bhatti agent tests on Pi via SSH.
#
# Usage:
#   ./scripts/test-on-pi.sh                    # all tests
#   ./scripts/test-on-pi.sh TestAgentTTY       # single test
#   PI_HOST=user@10.0.0.5 ./scripts/test-on-pi.sh  # different Pi
#
set -euo pipefail

PI_HOST="${PI_HOST:-user@192.168.1.201}"
PI_TMP="/tmp/bhatti-agent-test"
LOCAL_BIN="bin/bhatti-agent-test-linux-arm64"

TEST_RUN="${1:-}"
EXTRA_ARGS=""
if [[ -n "$TEST_RUN" ]]; then
    EXTRA_ARGS="-test.run=$TEST_RUN"
fi

echo "==> Cross-compiling test binary..."
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go test -c \
    -o "$LOCAL_BIN" ./cmd/bhatti-agent

echo "==> Uploading to $PI_HOST:$PI_TMP..."
scp -q "$LOCAL_BIN" "$PI_HOST:$PI_TMP"

echo "==> Running tests on Pi..."
ssh "$PI_HOST" "chmod +x $PI_TMP && $PI_TMP -test.v -test.timeout=60s $EXTRA_ARGS"
