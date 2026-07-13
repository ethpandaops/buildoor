#!/usr/bin/env bash
set -Eeuo pipefail

readonly ENCLAVE_NAME="${ENCLAVE_NAME:-buildoor-e2e}"
readonly ETHEREUM_PACKAGE="${ETHEREUM_PACKAGE:-github.com/ethpandaops/ethereum-package@39bdd0d78c3b094ddaa4f5c580062dfdf1e098c6}"
readonly ARGS_FILE="${ARGS_FILE:-.github/e2e/kurtosis.yaml}"
readonly ARTIFACT_DIR="${ARTIFACT_DIR:-${TMPDIR:-/tmp}/buildoor-e2e-artifacts}"
readonly TIMEOUT_SECONDS="${TIMEOUT_SECONDS:-600}"
readonly GLOAS_SLOT=8
readonly CL_SERVICE="${CL_SERVICE:-cl-1-lodestar-nethermind}"
readonly EL_SERVICE="${EL_SERVICE:-el-1-nethermind-lodestar}"

mkdir -p "$ARTIFACT_DIR"

dump_diagnostics() {
  local exit_code=$?
  trap - EXIT
  set +e
  kurtosis enclave inspect "$ENCLAVE_NAME" >"$ARTIFACT_DIR/enclave.txt" 2>&1
  kurtosis service logs --all-services --all "$ENCLAVE_NAME" >"$ARTIFACT_DIR/services.log" 2>&1
  if [[ "${KEEP_ENCLAVE:-false}" != "true" ]]; then
    kurtosis enclave rm --force "$ENCLAVE_NAME" >/dev/null 2>&1
  fi
  exit "$exit_code"
}
trap dump_diagnostics EXIT

endpoint() {
  local service=$1
  local port=$2
  local address
  address=$(kurtosis port print --format ip,number "$ENCLAVE_NAME" "$service" "$port" | tail -n 1)
  printf 'http://%s' "$address"
}

json_rpc() {
  local url=$1
  local method=$2
  local params=$3
  curl --fail --silent --show-error \
    -H 'Content-Type: application/json' \
    --data "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"$method\",\"params\":$params}" \
    "$url"
}

echo "Starting Kurtosis enclave $ENCLAVE_NAME"
kurtosis run "$ETHEREUM_PACKAGE" \
  --enclave "$ENCLAVE_NAME" \
  --args-file "$ARGS_FILE" \
  --image-download missing \
  --verbosity brief

BUILDOOR_URL=$(endpoint buildoor api)
BEACON_URL=$(endpoint "$CL_SERVICE" http)
EXECUTION_URL=$(endpoint "$EL_SERVICE" rpc)
readonly BUILDOOR_URL BEACON_URL EXECUTION_URL

echo "Buildoor API: $BUILDOOR_URL"
echo "Beacon API:   $BEACON_URL"
echo "Execution RPC: $EXECUTION_URL"

deadline=$((SECONDS + TIMEOUT_SECONDS))
until curl --fail --silent "$BUILDOOR_URL/eth/v1/builder/status" >/dev/null; do
  if (( SECONDS >= deadline )); then
    echo "Buildoor API did not become ready within ${TIMEOUT_SECONDS}s" >&2
    exit 1
  fi
  sleep 2
done

spec=$(curl --fail --silent --show-error "$BEACON_URL/eth/v1/config/spec")
jq -e '.data.PRESET_BASE == "minimal" and (.data.GLOAS_FORK_EPOCH | tonumber) == 1' \
  <<<"$spec" >/dev/null

echo "Waiting for pre-Gloas Builder API and post-Gloas ePBS wins"
while true; do
  if ! won_blocks=$(curl --fail --silent --show-error \
    "$BUILDOOR_URL/api/buildoor/bids-won?offset=0&limit=100"); then
    if (( SECONDS >= deadline )); then
      echo "Buildoor API became unavailable before both wins were observed" >&2
      exit 1
    fi
    sleep 3
    continue
  fi
  printf '%s\n' "$won_blocks" >"$ARTIFACT_DIR/bids-won.json"

  pre_block=$(jq -c --argjson fork_slot "$GLOAS_SLOT" \
    '[.bids_won[] | select(.source == "builder_api" and .slot < $fork_slot)] | first // empty' \
    <<<"$won_blocks")
  post_block=$(jq -c --argjson fork_slot "$GLOAS_SLOT" \
    '[.bids_won[] | select(.source == "epbs" and .slot >= $fork_slot)] | first // empty' \
    <<<"$won_blocks")

  if [[ -n "$pre_block" && -n "$post_block" ]]; then
    break
  fi
  if (( SECONDS >= deadline )); then
    echo "Did not observe both required won blocks within ${TIMEOUT_SECONDS}s" >&2
    jq '{total, bids_won}' <<<"$won_blocks" >&2
    exit 1
  fi
  sleep 3
done

verify_block() {
  local label=$1
  local block=$2
  local slot hash rpc_block beacon_block rpc_hash extra_data
  slot=$(jq -r '.slot' <<<"$block")
  hash=$(jq -r '.block_hash | ascii_downcase' <<<"$block")

  local verify_deadline=$((SECONDS + 60))
  while true; do
    rpc_block=$(json_rpc "$EXECUTION_URL" eth_getBlockByHash "[\"$hash\",false]")
    rpc_hash=$(jq -r '.result.hash // empty | ascii_downcase' <<<"$rpc_block")
    extra_data=$(jq -r '.result.extraData // empty | ascii_downcase' <<<"$rpc_block")
    if [[ "$rpc_hash" == "$hash" && "$extra_data" == 0x6275696c646f6f72* ]]; then
      break
    fi
    if (( SECONDS >= verify_deadline )); then
      echo "Execution RPC did not return the $label Buildoor block $hash" >&2
      exit 1
    fi
    sleep 2
  done

  while true; do
    beacon_block=$(curl --fail --silent --show-error "$BEACON_URL/eth/v2/beacon/blocks/$slot")
    if jq -e --arg hash "$hash" \
      '[.. | objects | .block_hash? // empty | ascii_downcase] | index($hash) != null' \
      <<<"$beacon_block" >/dev/null; then
      break
    fi
    if (( SECONDS >= verify_deadline )); then
      echo "Beacon API block at slot $slot did not reference $label payload $hash" >&2
      exit 1
    fi
    sleep 2
  done

  printf '%s\n' "$rpc_block" >"$ARTIFACT_DIR/${label}-execution-block.json"
  printf '%s\n' "$beacon_block" >"$ARTIFACT_DIR/${label}-beacon-block.json"
  echo "Verified $label won block at slot $slot ($hash) via Buildoor, beacon API, and execution RPC"
}

verify_block pre-gloas "$pre_block"
verify_block post-gloas "$post_block"

echo "Kurtosis end-to-end test passed"
