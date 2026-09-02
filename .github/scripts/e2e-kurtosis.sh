#!/usr/bin/env bash
set -Eeuo pipefail

readonly ENCLAVE_NAME="${ENCLAVE_NAME:-buildoor-e2e}"
readonly ETHEREUM_PACKAGE="${ETHEREUM_PACKAGE:-github.com/ethpandaops/ethereum-package@39bdd0d78c3b094ddaa4f5c580062dfdf1e098c6}"
readonly ARGS_FILE="${ARGS_FILE:-.github/e2e/kurtosis.yaml}"
readonly ARTIFACT_DIR="${ARTIFACT_DIR:-${TMPDIR:-/tmp}/buildoor-e2e-artifacts}"
# Budget for buildoor to come up once the enclave is running.
readonly READY_TIMEOUT_SECONDS="${READY_TIMEOUT_SECONDS:-300}"
# The pre-Gloas win can only happen in the slots before the fork, so its own
# budget is short; a miss there is never recovered by waiting longer.
readonly PREGLOAS_TIMEOUT_SECONDS="${PREGLOAS_TIMEOUT_SECONDS:-180}"
# The post-Gloas phases first have to wait out the builder's deposit becoming
# active on chain (a handful of epochs), so they get a much wider budget.
readonly POSTGLOAS_TIMEOUT_SECONDS="${POSTGLOAS_TIMEOUT_SECONDS:-600}"
readonly GLOAS_SLOT=8
readonly CL_SERVICE="${CL_SERVICE:-cl-1-lodestar-nethermind}"
readonly EL_SERVICE="${EL_SERVICE:-el-1-nethermind-lodestar}"

# Upper bound for "any later slot" win filters.
readonly MAX_SLOT=1000000000

# Slots a settings change needs before it is guaranteed to be in effect: a
# slot's action plan freezes about one slot before the slot executes, so the
# second slot after the flip is the first one certain to run under the new
# configuration.
readonly SETTINGS_LEAD_SLOTS=2

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

# The wall-clock slot as buildoor sees it. Taken from buildoor rather than the
# beacon head because it is buildoor's own clock that decides when a slot's
# action plan freezes, and the chain head lags behind it on a missed slot.
current_slot() {
  curl --fail --silent --show-error "$BUILDOOR_URL/api/status" | jq -r '.current_slot'
}

# Applies a partial global settings update by canonical settings key. The
# buildoor API runs open (no --auth-provider-url), so no token is needed.
update_settings() {
  local body=$1
  echo "Applying settings update: $body"
  curl --fail --silent --show-error \
    -H 'Content-Type: application/json' \
    --data "$body" \
    "$BUILDOOR_URL/api/config/settings" >/dev/null
}

# first_slot_under_new_settings prints the first slot whose plan is guaranteed
# to have frozen after the settings update that just went out.
first_slot_under_new_settings() {
  local floor=$1
  local from=$(( $(current_slot) + SETTINGS_LEAD_SLOTS ))
  if (( from < floor )); then
    from=$floor
  fi
  printf '%s' "$from"
}

# wait_for_win <label> <source> <min_slot> <max_slot> <timeout_seconds>
# Polls the won-block view until one of our blocks with the requested source
# shows up in the slot range, then prints that entry.
wait_for_win() {
  local label=$1
  local source=$2
  local min_slot=$3
  local max_slot=$4
  local timeout=$5
  local deadline=$((SECONDS + timeout))
  local won_blocks block

  echo "Waiting for a '$source' win in slots ${min_slot}..${max_slot} ($label)" >&2
  while true; do
    if won_blocks=$(curl --fail --silent --show-error \
      "$BUILDOOR_URL/api/buildoor/bids-won?offset=0&limit=100"); then
      printf '%s\n' "$won_blocks" >"$ARTIFACT_DIR/bids-won-$label.json"

      block=$(jq -c --arg source "$source" --argjson min "$min_slot" --argjson max "$max_slot" \
        '[.bids_won[] | select(.source == $source and .slot >= $min and .slot <= $max)] | first // empty' \
        <<<"$won_blocks")
      if [[ -n "$block" ]]; then
        printf '%s\n' "$block"
        return 0
      fi
    fi

    if (( SECONDS >= deadline )); then
      echo "Did not observe a '$source' win in slots ${min_slot}..${max_slot} within ${timeout}s" >&2
      jq '{total, bids_won}' <<<"${won_blocks:-null}" >&2
      exit 1
    fi
    sleep 3
  done
}

# assert_slot_result <label> <slot> <jq filter over the slot's SlotResult>
# Checks the attempt-level record of the slot, which is what actually proves
# WHICH flow produced the win: the won-block source alone is derived from the
# same records, so the filters below spell out the expected evidence. The
# tracker consumes the producing services' events asynchronously, so the record
# is polled until it satisfies the filter.
assert_slot_result() {
  local label=$1
  local slot=$2
  local filter=$3
  local results record
  local assert_deadline=$((SECONDS + 60))

  while true; do
    if results=$(curl --fail --silent --show-error \
      "$BUILDOOR_URL/api/buildoor/slot-results?min_slot=$slot&max_slot=$slot"); then
      printf '%s\n' "$results" >"$ARTIFACT_DIR/${label}-slot-result.json"

      # SlotResult.slot is a JSON string, so compare it numerically.
      record=$(jq -c --argjson slot "$slot" \
        '[.results[] | select((.slot | tonumber) == $slot)] | first // empty' <<<"$results")
      if [[ -n "$record" ]] && jq -e "$filter" <<<"$record" >/dev/null; then
        echo "Slot result evidence for the $label slot $slot checks out"
        return 0
      fi
    fi

    if (( SECONDS >= assert_deadline )); then
      echo "Slot result for the $label slot $slot does not match: $filter" >&2
      jq '.' <<<"${record:-null}" >&2
      exit 1
    fi
    sleep 2
  done
}

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

deadline=$((SECONDS + READY_TIMEOUT_SECONDS))
until curl --fail --silent "$BUILDOOR_URL/eth/v1/builder/status" >/dev/null; do
  if (( SECONDS >= deadline )); then
    echo "Buildoor API did not become ready within ${READY_TIMEOUT_SECONDS}s" >&2
    exit 1
  fi
  sleep 2
done

spec=$(curl --fail --silent --show-error "$BEACON_URL/eth/v1/config/spec")
jq -e '.data.PRESET_BASE == "minimal" and (.data.GLOAS_FORK_EPOCH | tonumber) == 1' \
  <<<"$spec" >/dev/null

# Post-Gloas both flows offer the proposer the same payload, so a slot where
# both are active cannot tell us which one the proposer actually used. The
# post-Gloas phases therefore run one flow at a time, toggled through the
# settings API, and only accept wins from slots frozen after the toggle.

echo "== Phase 1: pre-Gloas Builder API (getHeader / blinded block)"
pre_block=$(wait_for_win pre-gloas builder_api 0 $((GLOAS_SLOT - 1)) "$PREGLOAS_TIMEOUT_SECONDS")
pre_slot=$(jq -r '.slot' <<<"$pre_block")
verify_block pre-gloas "$pre_block"
assert_slot_result pre-gloas "$pre_slot" \
  '((.block_submissions // []) | map(select(.dialect == "legacy" and .status == "accepted")) | length > 0)'

echo "== Phase 2: post-Gloas p2p bidding (Builder API suppressed)"
update_settings '{"builder_api_enabled": false}'
p2p_from=$(first_slot_under_new_settings "$GLOAS_SLOT")
p2p_block=$(wait_for_win post-gloas-p2p epbs "$p2p_from" "$MAX_SLOT" "$POSTGLOAS_TIMEOUT_SECONDS")
p2p_slot=$(jq -r '.slot' <<<"$p2p_block")
verify_block post-gloas-p2p "$p2p_block"
assert_slot_result post-gloas-p2p "$p2p_slot" \
  '((.bids // []) | map(select(.transport == "p2p" and .status == "submitted")) | length > 0)
   and ((.bids // []) | map(select(.transport == "builder-api" and .status == "served")) | length == 0)
   and ((.reveal_attempts // []) | map(select(.status == "published")) | length > 0)'

echo "== Phase 3: post-Gloas Builder API (p2p bidding suppressed)"
update_settings '{"builder_api_enabled": true, "epbs_enabled": false}'
api_from=$(first_slot_under_new_settings "$GLOAS_SLOT")
api_block=$(wait_for_win post-gloas-builder-api builder_api "$api_from" "$MAX_SLOT" "$POSTGLOAS_TIMEOUT_SECONDS")
api_slot=$(jq -r '.slot' <<<"$api_block")
verify_block post-gloas-builder-api "$api_block"
# The proposer took the bid it fetched over HTTP, handed the signed block back
# to buildoor (epbs dialect) and buildoor revealed the envelope for it.
assert_slot_result post-gloas-builder-api "$api_slot" \
  '((.bids // []) | map(select(.transport == "builder-api" and .status == "served")) | length > 0)
   and ((.bids // []) | map(select(.transport == "p2p" and .status == "submitted")) | length == 0)
   and ((.block_submissions // []) | map(select(.dialect == "epbs" and .status == "accepted")) | length > 0)
   and ((.reveal_attempts // []) | map(select(.status == "published")) | length > 0)'

# The Lodestar VC submits builder preferences (max_execution_payment) ahead of
# every proposal; a stored entry proves submitBuilderPreferences accepts the
# authenticated requests. Bid serving alone cannot show this — bids kept
# working while the preferences endpoint 500'd without --builder-api-url.
echo "Checking that proposer builder preferences were accepted"
prefs_deadline=$((SECONDS + 60))
until curl --fail --silent --show-error "$BUILDOOR_URL/api/buildoor/builder-preferences" \
  | tee "$ARTIFACT_DIR/builder-preferences.json" \
  | jq -e '(.preferences | length) > 0' >/dev/null; do
  if (( SECONDS >= prefs_deadline )); then
    echo "No builder preferences were stored — submitBuilderPreferences is failing" >&2
    exit 1
  fi
  sleep 2
done
echo "Builder preferences present"

echo "Kurtosis end-to-end test passed"
