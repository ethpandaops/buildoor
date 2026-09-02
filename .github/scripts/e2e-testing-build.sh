#!/usr/bin/env bash
# Testing build source e2e: spins a minimal gloas@1 devnet with a geth-backed
# buildoor, switches the builder to the testing source, feeds its tx intake
# with spamoor, and requires that every won block after the switch verified
# as an exact tx plan match (same transactions, same order, same count) and
# that at least MIN_FILLED_BLOCKS of them carried transactions.
set -Eeuo pipefail

readonly ENCLAVE_NAME="${ENCLAVE_NAME:-buildoor-testing-e2e}"
readonly ETHEREUM_PACKAGE="${ETHEREUM_PACKAGE:-github.com/ethpandaops/ethereum-package}"
readonly ARGS_FILE="${ARGS_FILE:-.github/e2e/kurtosis-testing-build.yaml}"
readonly ARTIFACT_DIR="${ARTIFACT_DIR:-${TMPDIR:-/tmp}/buildoor-testing-e2e-artifacts}"
readonly TIMEOUT_SECONDS="${TIMEOUT_SECONDS:-900}"
readonly MIN_FILLED_BLOCKS="${MIN_FILLED_BLOCKS:-6}"
readonly MIN_TXS_PER_BLOCK="${MIN_TXS_PER_BLOCK:-20}"
readonly SPAMOOR_BIN="${SPAMOOR_BIN:-spamoor}"
readonly SPAMOOR_PRIVKEY="${SPAMOOR_PRIVKEY:-39725efee3fb28614de3bacaffe4cc4bd8c436257e2c8bb887c4b5c4be45e76d}"
readonly BUILDOOR_SERVICE="${BUILDOOR_SERVICE:-buildoor-lighthouse-geth-1}"
readonly EL_SERVICE="${EL_SERVICE:-el-1-geth-lighthouse}"

mkdir -p "$ARTIFACT_DIR"
spamoor_pid=""

dump_diagnostics() {
  local exit_code=$?
  trap - EXIT
  set +e
  [[ -n "$spamoor_pid" ]] && kill "$spamoor_pid" 2>/dev/null
  kurtosis enclave inspect "$ENCLAVE_NAME" >"$ARTIFACT_DIR/enclave.txt" 2>&1
  kurtosis service logs --all-services --all "$ENCLAVE_NAME" >"$ARTIFACT_DIR/services.log" 2>&1
  if [[ "${KEEP_ENCLAVE:-false}" != "true" ]]; then
    kurtosis enclave rm --force "$ENCLAVE_NAME" >/dev/null 2>&1
  fi
  exit "$exit_code"
}
trap dump_diagnostics EXIT

endpoint() {
  local address
  address=$(kurtosis port print --format ip,number "$ENCLAVE_NAME" "$1" "$2" | tail -n 1)
  printf 'http://%s' "$address"
}

if [[ "${SKIP_ENCLAVE_START:-false}" != "true" ]]; then
  echo "Starting Kurtosis enclave $ENCLAVE_NAME"
  kurtosis run "$ETHEREUM_PACKAGE" \
    --enclave "$ENCLAVE_NAME" \
    --args-file "$ARGS_FILE" \
    --image-download missing \
    --verbosity brief
fi

BUILDOOR_URL=$(endpoint "$BUILDOOR_SERVICE" api)
EXECUTION_URL=$(endpoint "$EL_SERVICE" rpc)
readonly BUILDOOR_URL EXECUTION_URL
echo "Buildoor API:  $BUILDOOR_URL"
echo "Execution RPC: $EXECUTION_URL"

deadline=$((SECONDS + TIMEOUT_SECONDS))
until curl --fail --silent "$BUILDOOR_URL/api/status" >/dev/null; do
  (( SECONDS < deadline )) || { echo "Buildoor API not ready" >&2; exit 1; }
  sleep 2
done

echo "geth must serve the testing namespace"
curl --fail --silent -H 'Content-Type: application/json' \
  --data '{"jsonrpc":"2.0","id":1,"method":"rpc_modules","params":[]}' "$EXECUTION_URL" \
  | jq -e '.result.testing' >/dev/null

echo "Switching the build source to testing"
curl --fail --silent -X POST -H 'Content-Type: application/json' \
  --data '{"source":"testing","fill_gas_pct":100,"policy":"fifo"}' \
  "$BUILDOOR_URL/api/config/testing" | jq -e '.status == "updated"' >/dev/null
switch_slot=$(curl --fail --silent "$BUILDOOR_URL/api/status" | jq -r '.current_slot')
echo "Switched at slot $switch_slot"

echo "Feeding the tx intake with spamoor eoatx"
"$SPAMOOR_BIN" eoatx \
  --privkey "$SPAMOOR_PRIVKEY" \
  --rpchost "$BUILDOOR_URL/rpc" \
  -t 300 --max-pending 3000 --max-wallets 30 \
  --basefee 100 --tipfee 2 --refill-amount 50 --refill-balance 10 --rebroadcast 30 \
  >"$ARTIFACT_DIR/spamoor.log" 2>&1 &
spamoor_pid=$!

echo "Waiting for $MIN_FILLED_BLOCKS verified full blocks (>= $MIN_TXS_PER_BLOCK txs each)"
from_slot=$((switch_slot + 2))
while true; do
  current_slot=$(curl --fail --silent "$BUILDOOR_URL/api/status" | jq -r '.current_slot')
  if (( current_slot < from_slot )); then
    sleep 6
    continue
  fi

  results=$(curl --fail --silent "$BUILDOOR_URL/api/buildoor/slot-results?min_slot=$from_slot&max_slot=$current_slot")
  printf '%s\n' "$results" >"$ARTIFACT_DIR/slot-results.json"

  # Every included plan after the switch must have verified as a match.
  bad=$(jq -c '[.results[] | select(.inclusion != null and .tx_plan != null)
                 | select(.tx_plan.status != "match" and .tx_plan.status != "pending")
                 | {slot, status: .tx_plan.status, detail: .tx_plan.detail}]' <<<"$results")
  if [[ "$bad" != "[]" ]]; then
    echo "TX PLAN CHECK FAILED: $bad" >&2
    exit 1
  fi

  filled=$(jq --argjson min "$MIN_TXS_PER_BLOCK" \
    '[.results[] | select(.tx_plan != null and .tx_plan.status == "match" and .tx_plan.included_count >= $min)] | length' <<<"$results")
  echo "slot $current_slot: verified full blocks so far: $filled"
  if (( filled >= MIN_FILLED_BLOCKS )); then
    break
  fi

  if (( SECONDS >= deadline )); then
    echo "Only $filled verified full blocks within ${TIMEOUT_SECONDS}s" >&2
    jq -c '.results[] | {slot, build: .build.status, txs: .build.num_transactions, plan: .tx_plan.status, included: .tx_plan.included_count}' <<<"$results" >&2
    exit 1
  fi

  sleep 6
done

echo "Cross-checking one verified block on the EL"
block=$(jq -c '[.results[] | select(.tx_plan != null and .tx_plan.status == "match" and .tx_plan.included_count >= 1)] | last' <<<"$results")
hash=$(jq -r '.inclusion.block_hash' <<<"$block")
expected=$(jq -c '.tx_plan.expected_hashes | map(ascii_downcase)' <<<"$block")
actual=$(curl --fail --silent -H 'Content-Type: application/json' \
  --data "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"eth_getBlockByHash\",\"params\":[\"$hash\",false]}" "$EXECUTION_URL" \
  | jq -c '.result.transactions | map(ascii_downcase)')
if [[ "$expected" != "$actual" ]]; then
  echo "EL block $hash transaction list differs from the plan" >&2
  echo "expected: $expected" >&2
  echo "actual:   $actual" >&2
  exit 1
fi

queue=$(curl --fail --silent "$BUILDOOR_URL/api/buildoor/tx-queue")
printf '%s\n' "$queue" >"$ARTIFACT_DIR/tx-queue.json"
jq -e '.plan_checks.mismatch == 0 and .plan_checks.block_not_found == 0' <<<"$queue" >/dev/null

echo "OK: $filled buildoor blocks verified as exact tx plan matches; plan checks: $(jq -c '.plan_checks' <<<"$queue")"
