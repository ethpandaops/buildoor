# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Overview

Buildoor is an Ethereum builder tool supporting both **ePBS (enshrined proposer-builder separation)** and **traditional Builder API** modes. It connects to consensus layer (beacon) and execution layer clients to build blocks, submit bids, and manage builder lifecycle operations.

## Commands

### Building
```bash
# Build the binary
make build

# Clean build artifacts
make clean

# Generate API documentation (Swagger)
make docs

# Run all tests
make test
```

### Running
```bash
# Basic run (minimal config)
go run main.go run \
  --builder-privkey <BLS_PRIVATE_KEY> \
  --cl-client <BEACON_NODE_URL> \
  --el-engine-api <ENGINE_API_URL> \
  --el-jwt-secret <JWT_SECRET_PATH>

# Run with ePBS enabled
go run main.go run \
  --builder-privkey <BLS_PRIVATE_KEY> \
  --cl-client <BEACON_NODE_URL> \
  --el-engine-api <ENGINE_API_URL> \
  --el-jwt-secret <JWT_SECRET_PATH> \
  --epbs \
  --epbs-bid-start 1000 \
  --epbs-bid-end 3000 \
  --reveal-time 4000

# Run with lifecycle management (deposits/exits)
go run main.go run \
  --builder-privkey <BLS_PRIVATE_KEY> \
  --cl-client <BEACON_NODE_URL> \
  --el-engine-api <ENGINE_API_URL> \
  --el-jwt-secret <JWT_SECRET_PATH> \
  --el-rpc <EXECUTION_RPC_URL> \
  --wallet-privkey <ECDSA_PRIVATE_KEY> \
  --lifecycle

# Run with Builder API (pre-ePBS mode)
go run main.go run \
  --builder-privkey <BLS_PRIVATE_KEY> \
  --cl-client <BEACON_NODE_URL> \
  --el-engine-api <ENGINE_API_URL> \
  --el-jwt-secret <JWT_SECRET_PATH> \
  --builder-api-enabled \
  --api-port 18550

# Run with WebUI dashboard
go run main.go run \
  --builder-privkey <BLS_PRIVATE_KEY> \
  --cl-client <BEACON_NODE_URL> \
  --el-engine-api <ENGINE_API_URL> \
  --el-jwt-secret <JWT_SECRET_PATH> \
  --api-port 8082
```

### Testing
```bash
# Run all tests
go test ./...

# Run tests for specific package
go test ./pkg/payload_builder

# Run tests with verbose output
go test -v ./pkg/payload_builder

# Run specific test
go test -v ./pkg/payload_builder -run TestPayloadBuilder
```

### Docker
```bash
# Build Docker image
make docker

# Run Docker container
make docker-run
```

### Development Environment
```bash
# Start local devnet
make devnet

# Run builder against devnet
make devnet-run

# Clean up devnet
make devnet-clean
```

### Frontend Development
```bash
# Navigate to webui directory
cd pkg/webui

# Install dependencies
npm install

# Build production bundle
npm run build

# Development mode (watch for changes)
npm run dev

# Clean build artifacts
npm run clean
```

## Architecture

### Core Components

0. **Action Plan Service** (`pkg/action_plan/`) — the per-slot scheduling authority
   - Sparse, persisted per-slot operation modes for three consumer categories:
     `bid` (p2p), `builder_api` (bid serving) and `reveal`. A category is an explicit
     instruction: absent = inherit the global baseline (incl.
     `epbs_enabled`/`builder_api_enabled`), `disabled` = suppress for the slot,
     `custom` = force-ACTIVE for the slot (even when the module is globally disabled)
     with optional setting overrides. Availability still wins over plans (API port,
     fork at slot, registration, signer).
   - A fourth `build` category is MODELESS (no custom/disabled — just build tweaks):
     `reorg_parent_payload` builds the slot's payload on the grandparent (n-2)
     execution payload instead of the immediate parent. payload_builder sources the
     parent block hash, parent block NUMBER and withdrawals from the PARENT slot's
     cached payload attributes (whose parent is n-2) while keeping every other
     property — incl. the beacon parent root — from the current slot, so the built
     payload AND the bid derived from it agree on the reorged parent. A deliberate
     parent-payload reorg (invalid on mainnet forkchoice; a testing knob). Falls back
     to a normal build (logged) when the parent slot's attributes are unavailable.
     It only modifies HOW a build happens, never forces/suppresses the build decision.
   - A fifth `transforms` category is MODELESS: operator-supplied jq expressions
     (`payload`/`bid`/`envelope`) applied to the object's JSON via `pkg/jqtransform`
     (wraps `itchyny/gojq`; env access disabled, single-output, ctx-timeout 2s) for
     arbitrary custom builder testing. Payload rewrites the built execution payload
     before it feeds both the bid commitment and the reveal; the block hash is
     recomputed from the transformed header fields unless the expression sets
     `block_hash` itself (keep that for deliberate hash-mismatch tests), so field
     edits yield execution-invalid but hash-valid payloads; bid/envelope rewrite the MESSAGE just before signing
     and are then RE-SIGNED (target-slot fork) — so results are validly signed but
     customized, and a bid commitment can deliberately diverge from the revealed
     payload. Expressions are jq-Validated at plan-update time (400 on bad jq); a
     runtime transform failure fails that construction (loud, recorded). Round-trip is
     MarshalJSON→gojq→UnmarshalJSON into a fresh object with Version preset (via
     `jqtransform.ApplyTyped`). Live-tested from the UI through
     `POST /api/v1/action-plan/test-transform` (target+expression+optional
     sample_slot → runs the exact production gojq against a captured artifact, else
     the latest buffered one, else an illustrative template; bid/envelope reduced to
     `.message`).
   - **Recurring rules** (`rules.go`) cover what sparse plans cannot: a scenario
     that must repeat for a whole devnet run. A `SlotRule` matches slot INDICES
     within the epoch (`slots_in_epoch`, optionally bounded by `from_epoch`/
     `to_epoch`) and carries the very same categories as a `SlotPlan` — e.g.
     win slot 31 of every epoch (`bid: custom` + value override) and withhold
     its payload (`reveal: disabled`), the ePBS analogue of "run a devnet with
     slot 31 missing". Precedence is WHOLESALE, never merged: an explicit plan
     for a slot replaces any rule, and `SlotPlan.IgnoreRules` opts a single slot
     out entirely (which is why such a plan is not "empty"). Rules resolve at
     freeze/read time (`PlanForSlot`), so a rule change never rewrites an
     already frozen slot, and the materialized plan is marked with `rule_id` —
     it is synthesized on every read, never persisted per slot. Several matching
     rules: lowest id wins. The set is replaced atomically (`SetRules`, max 32),
     persisted via the `kv_store` `slot_rules` namespace, and fires
     `RuleChangeEvent`
   - **Freeze semantics**: `Freeze(slot)` resolves the immutable `FrozenPlan` (raw plan
     + effective settings merged from the live global config + target-slot fork + the
     COMPLETE build decision incl. schedule modes and next_n accounting) the first time
     any decision point touches the slot; all later callers get the identical snapshot.
     Consumers (payload_builder, p2p_bidder, RevealService, builderapi) poll frozen
     snapshots — no module interprets raw plans or schedule config itself. Edits to
     past or frozen slots fail with `ErrSlotLocked` (HTTP 409). Slots freeze in
     practice ~1 slot ahead (payload_attributes / first scheduler tick); the Builder
     API bid handlers reject slots beyond `currentSlot+1` BEFORE freezing so clients
     cannot lock future plans.
   - Bulk mutations (`ApplyUpdates`) are atomic all-or-nothing, support slot lists +
     inclusive ranges, three-state category patches (absent/null/object) and
     fine-grained `set` paths (`"bid.bid_min_amount": 5000`); overlapping updates
     apply in request order; committed changes fire `PlanChangeEvent`.
   - Persisted via the `kv_store` `slot_plans` namespace; past plans prune to
     `slot-result-retention-epochs`, future plans never. `GetRange` returns the
     EFFECTIVE plans of a range: stored plans plus the rule-derived plans of the
     still-open slots (`> currentSlot`, not yet frozen) — past slots keep their
     recorded history instead of being described by today's rules.

0b. **Chain view / head tracker** (`pkg/chain/headtracker.go`) — buildoor's own
   canonical-chain oracle: current head, bounded block-by-root ancestry cache
   (shared with the inclusion tracker), per-block Gloas payload reveal status
   (payload-available events + child-block evidence + PAYLOAD_DUE fallback),
   reorg detection (head-change events with depth/common-ancestor; the beacon
   `chain_reorg` SSE topic is subscribed as a cross-check), and build-parent
   candidate resolution. Also: epoch-stats refetch when a head event's duty
   dependent root proves a reorg crossed the epoch boundary.

0c. **Build candidates** — a slot may build SEVERAL payloads, one per parent
   tuple (candidate keys: `parent_full`, `parent_empty` = Gloas payload miss,
   `grandparent_full` = deliberate reorg of the head block,
   `grandparent_empty`). Policy per key: `auto` (chain signals: parent payload
   reveal status, head-vote weakness below `build.auto_weak_head_pct`) /
   `always` / `never` via `build.candidate_*` settings, per-slot overridable
   through the action plan's `build.candidates` map (resolved into
   `FrozenPlan.Build.CandidateModes`). Attributes are cached per
   (slot, parent_root, parent_hash) variant — last-writer-wins per variant —
   sanitized against the chain view (Grandine's wrong miss-case parent is
   corrected, missing parent numbers backfilled) and synthesized for uncovered
   candidates (empty-parent withdrawals from the parent slot's attributes;
   grandparent candidates re-target the parent slot's attributes, refused
   across epoch boundaries). Candidate builds run concurrently by default
   (`build.parallel`); sequential builds run the canonical candidate first
   (it keeps its scheduled start), speculative ones after. Consumers select
   at request time: p2p bids per `epbs.bid_candidate` (auto = match chain view, sticky per slot unless
   `epbs.bid_candidate_switch`; `all` = deliberate multi-parent gossip),
   builder-api serves whichever candidate matches the requested parent
   (`builder_api.serve_candidates` policy, optional
   `builder_api.on_demand_build`). Reveals re-bind (rebuilt, re-signed
   envelope) when a won payload is re-included under a different block root
   (`reveal.rebind_on_reorg`); orphaned wins dispute pending payments and
   clear won markers until the block returns. Flag-gated
   `build.enforce_bid_gas_limit` adjusts built payloads to the exact
   bid-gossip-legal gas limit (EL parent gas limit stepped toward the
   proposer target).

0d. **Local build + owned transaction pool** — opt-in extensions, both off by
   default and both riding on the EL JSON-RPC client (`--el-rpc`).
   - **Local build** (`payload_builder/localbuild.go`, `rpc/execution/testing_build.go`):
     an ADDITIONAL payload per build target through the EL's `testing_buildBlockV1`
     (execution-apis `testing` namespace — public RPC port, disabled by default on
     every client, never on authrpc). It starts once the engine build's
     forkchoiceUpdated pinned the EL head to the parent (geth and ethrex require
     `parentHash == head`) and runs during the engine build's wait; without an
     engine build (engine builder off, `build.engine_enabled=false`) a
     head-only FCU is issued first. Only the CANONICAL candidate (`parent_full`,
     or an unclassified tuple) builds locally: geth's testing call builds on the
     EL's current head, and a speculative parent (`parent_empty`,
     `grandparent_*`) is not the head — the CL's own forkchoice updates and
     parallel candidate builds move it — so those targets skip with
     `speculative_candidate` (payload source `local` then yields no payload for
     them; `local_or_el` falls back). `local_build.payload_source` is the rule of
     the target's PICK (below): `el` (default; the testing payloads are built
     and recorded for inspection only), `local` (strict — no payload when the local
     build fails), `local_or_el` (fallback, recorded), `higher_value` (both
     built, the higher EL-reported block value wins; engine on a tie, the
     surviving one when a build fails). The ENGINE BUILDER switch
     `build.engine_enabled` (`--engine-build-enabled`, default on; per-slot
     plan `build.local.build_el_payload`; the toggle of the Engine builder
     card) turns the engine build off: only the head-pinning FCU remains, the
     testing build alone feeds the consumers whatever the payload source, and
     a target without a testing build is skipped quietly. Tx source (the
     FOCIL inclusion list always goes first, see 0e):
     `txpool` (the private pool alone, in `txpool.ordering`) |
     `most_profitable` (private pool and EL mempool merged into one
     selection by effective tip: one queue per sender across both pools in
     nonce order, of two txs at one nonce the higher tip; the ordering does
     not apply; a refusal retries without the public txs and their senders'
     later txs, `withoutSenderTails`) |
     `private_first` (formerly `txpool_el_fill`, the old value still maps:
     `NormalizedTxSource`, persisted overrides via `retiredValues`; pool
     selection first, then the EL mempool's
     pending txs fill the remaining gas — same parent-state checks, each
     sender continuing at the nonce/balance the pool left, highest tip first,
     pool duplicates skipped, txs below the next base fee dropped BEFORE any
     sender's state is read. The txs come from the pool's EL mempool MIRROR
     (`txpool/elmirror.go`), not a txpool_content read per build: decoded
     and sender-recovered once, arrivals LIVE over `--el-ws`
     (`eth_subscribe newPendingTransactions` with full txs; reconnect with
     backoff, one filter poll after each subscribe covers the gap) or else
     via `eth_newPendingTransactionFilter` (500 ms poll, also while the
     websocket is down; both report blob txs, which geth's txpool_content
     omits), removals by included block (+ lower nonces), stale nonces found
     by the selection and a full txpool_content resync every 5 min (blob
     entries, which the resync cannot confirm, expire by age); it starts on
     the first use and stops after 10 idle minutes. The EL mempool lists
     blob txs without their blobs, so the SELECTED ones are completed from the
     EL blob pool via `engine_getBlobs` (V1 pre-Osaka, V2 after: one batch
     request, per-tx fallback since it answers all-or-nothing; commitments
     computed and checked against the versioned hashes — `payload_builder/
     blobsidecars.go`), an incomplete one is dropped with its sender's later
     fill txs (`blob_unavailable`); a refusal
     retries without the fill's blob txs first (`fill_blobs_dropped`), then
     without the fill, an unreadable EL mempool builds from the pool alone)
     **Testing build timing** (`payload_builder/localtiming.go`): the call is
     not incremental, and the testing builds have their OWN window
     (`local_build.start_time_ms`/`end_time_ms`, slot-relative; the engine
     build keeps `build_start_time` + `payload_build_time`). A build starts
     only when estimated to finish by the window end (exponentially weighted
     fit of selection ms + call ms over gas, +2σ of its residuals; seeds
     until 3 builds; the selection that starts the mirror is left out).
     `rebuild_interval_ms` 0 = one build timed to the window end; > 0 (pool
     sources) = build at the window start, rebuild whenever
     `Pool.ContentVersion` moved (pool changes + mirror arrivals clearing the
     latest base fee), at least the interval apart. The testing loop has its
     own lifetime (`startLocalLoop`: window end + 5 s, cancelled like a build
     by a newer tuple build, a newer slot or `AbortBuild`). There is no
     hand-over moment: no build waits for another, the target's pick is
     re-evaluated whenever one of its builds finishes (see "Builds, payloads,
     picks" under 1). Every run is drawn with its own payload in the slot
     graph, the engine and testing windows as bluish bands. A build still
     running at its window end is not cut off (the EL finishes it anyway), so
     its duration still trains the model. | `empty` (`[]`) | `el_mempool` (`null`) | per-slot `explicit`
     (action plan `build.local.transactions`, validated at plan-update time). FOCIL
     inclusion-list txs are prepended; a failure with them retries once without
     (flagged `inclusion_list_dropped`). Every build reports its own
     payload (`Payload.Build` = engine | testing run N, `Payload.Local` the
     testing run's assembly), see "Builds, payloads, picks" under 1. Every EL either fails the whole call on
     one invalid tx (geth, ethrex), drops + post-checks (nethermind, besu) or
     silently filters (erigon → `dropped_by_el` recorded), hence the strict
     state-aware selection below.
   - **Enabling the namespace under kurtosis**: the ethereum-package appends
     `el_extra_params` AFTER its own module flag. geth, besu, erigon and ethrex
     take the repeated flag (last wins), nethermind fails ("expects a single argument but
     2 were provided") — use `--JsonRpc.AdditionalRpcUrls=http://0.0.0.0:8547|http|net;eth;web3;txpool;testing`
     and point `--el-rpc` at 8547 (what `.github/e2e/kurtosis.yaml` does) — and
     reth fails ("cannot be used multiple times") with no workaround. The local
     devnet therefore runs the buildoor-under-test participant on geth. Verified
     building through the local path on the devnet: geth, nethermind (extra
     endpoint), besu, erigon, ethrex; reth reports unavailable as designed.
     `kurtosis service update --cmd` cannot be used to add the flag later: it
     rebuilds the container without its file mounts.
   - **Availability gating**: `execution.Client.ProbeTestingAPI` calls the method
     with garbage params — `-32601` = unavailable, anything else = available —
     at startup, every 5 min (with the EL client version refresh) and via
     `POST /api/v1/local-build/probe`. The builder service implements
     `config.SettingGuard`: `local_build.enabled=true` is refused (400) while
     unavailable, `txpool.enabled=true` without `--el-rpc`. At build time
     availability wins over plans (skip reason `unavailable`, engine payload used).
     The EL identity (`engine_getClientVersionV1` code) selects the blob tx
     encoding (`network` = sidecar carried: geth/besu/nethermind; `canonical`:
     reth/ethrex, whose testing path returns NO blobs bundle → blob txs skipped as
     `blob_unsupported` unless `local_build.allow_blobs_without_bundle`).
   - **Transaction pool** (`pkg/txpool/`): owned mempool, runtime-toggleable
     (`txpool.enabled`, content kept while off; persisted across restarts with
     `--txpool-store`, see the state-db section). **Cold tier**
     (`txpool/tier.go`): a transaction whose fee cap is below the next base
     fee · (7/8)^16 (≈ 12%, at least 16 blocks from includable) keeps only its
     facts (nonce, fees, gas, value, to, blobs) in memory — `PooledTx.Tx` is
     nil, read through the `PooledTx` accessors — and its body waits in the
     pool store (pool) or the EL (mirror; cold arrivals are never held in
     full); at ≥ (7/8)^12 (≈ 20%) it is loaded back (`Pool.Get`/`Hydrated`
     load a cold body on demand). Re-tiered on every head from the head's
     next base fee; entries are swapped, never mutated. Without
     `--txpool-store` the pool keeps every body in memory. The selection
     treats a cold transaction as below the base fee. Admission is STATELESS (decode,
     chain id, signature, sidecar required for blob txs and checked against the
     versioned hashes, converted to cell proofs once from Osaka on, size, caps, same-nonce
     replacement needs a 10% fee bump; duplicates answer `already known` — the
     substring spamoor treats as success). Nonce/balance/fee floors are checked at
     SELECTION time against the parent state (`Select`: batched
     `eth_getTransactionCount`/`eth_getBalance` at the parent hash, next base fee
     from the parent header (`txpool/basefee.go`: from Heze/Bogota the
     EIP-8198 slot-spaced rule, max change `min(Δs, 12·Δslots) / (8·12·Δslots)`
     = 3/32 at 9s, the spacing read from the slot timeline; identical to 1/8
     at a 12s spacing), `eth_blobBaseFee`; per-sender contiguous runs merged
     `fifo` | `tip` | `random` under gas/blob/count budgets). The eviction loop
     follows beacon head + payload-available events: included txs are dropped
     (`included_by_us` vs `included_by_other` via `NoteBuiltBlock`), touched
     senders' passed nonces pruned, the TTL applied (`txpool.tx_ttl_slots`,
     default 64, 0 = never): an expired tx marks a stalled sender chain and
     is dropped TOGETHER with the sender's higher nonces — dropping it alone
     would leave a gap they could never cross — while fresh lower nonces
     stay; the generator restarts at the pool-aware "pending" nonce. Ingress (`pkg/txpool/rpc/`):
     JSON-RPC 2.0 (single + batch) at `POST /rpc` (or `/rpc/<key>`) on the API
     port. **Access** (`access.go`): a caller is an API key (WebUI-managed,
     `X-API-Key` / `Authorization: Bearer` / `/rpc/<key>`, SHA-256 stored in the
     `kv_store` `rpc_api_keys` namespace, secret shown once), the static
     `--txpool-auth-token`, a WebUI login (authenticatoor JWT; never when no
     auth provider is configured) or anonymous — allowed only with
     `txpool.auth=open` (`api_key` requires credentials; the retired
     `auth_token`/`static` values map to `api_key`). A presented invalid or
     disabled key is a 401. Per-call token-bucket rate limits (burst = 1 s):
     anonymous `txpool.rpc_anon_rate` per client IP or `shared`
     (`txpool.rpc_anon_scope`; client IP from X-Forwarded-For
     `txpool.rpc_proxy_hops` from the right), keys `txpool.rpc_key_rate` or
     their own override, logins unlimited; over the limit = `-32005`. Per-caller
     stats (calls, errors, rate-limited, per-method) in memory, anonymous IPs
     LRU-bounded, served with the keys and upstreams by
     `GET /api/v1/rpc-access`. **Reads** go through the upstream pool
     (`upstream.go`, `txpool.rpc_upstreams` = `<local|url>@<priority>,…`):
     lowest priority first, equal priorities round robin, transport failures
     fail over, JSON-RPC errors are answers; the local EL is just a member.
     **Privacy**: pool contents never leave to unauthorized readers —
     `txpool_content` / pool hits of `eth_getTransactionByHash` show operators
     (static token, login) everything, a key only what it submitted
     (`PooledTx.Submitter`), anonymous callers nothing. Methods:
     `eth_sendRawTransaction` (network encoding), `eth_chainId`, `net_version`,
     `web3_clientVersion`, pool-aware `eth_getTransactionCount(..,"pending")`,
     `eth_getTransactionByHash`, `txpool_status/content` locally; an allowlist
     of the standard public reads (`passthroughMethods`: eth_ state/block/receipt/
     log/fee/call reads, `net_listening`, `web3_sha3`) proxied through the
     upstreams — no privileged namespace, no signing/sending/bundles, no
     node-local answers (accounts, coinbase, the EL's pending txs), no filters or
     subscriptions; everything else
     `-32601`. The same privacy rule covers the WebUI: the pool REST
     (`/api/v1/txpool[/preview]`) needs a login, custom-built payload
     artifacts (and the envelope of a slot that has one) need a login, and
     explicit plan transaction lists are redacted to `transactions_redacted`
     for readers without a login (REST) and always on SSE; saving a redacted
     list back is refused.
     The route exists whenever `--el-rpc` is set, but while `txpool.enabled`
     is false EVERY call (read passthrough included) answers `txpool disabled`,
     so the EL is never reachable through the API port until the pool is on.
     buildoor must be the generator's ONLY host (`-h "name(buildoor)http://…/rpc"`):
     spamoor fans every submission out to all its hosts, so an additional EL host
     leaks the transactions into the EL mempool (visible as
     `evicted_included_by_other` on the pool stats).
   - **Post-inclusion verification** (`pkg/tx_plan_verifier`): every included
     payload built from an explicit list (`Payload.Local.ExpectedHashes`) is
     re-read from the EL and compared with the plan; the verdict lands on
     `SlotResult.tx_plan` (pending → match | mismatch | block_not_found |
     not_included after 20 slots | missed | orphaned mirrored from the Gloas
     payload verdicts), `buildoor_tx_plan_checks_total`, and a `TX PLAN CHECK
     FAILED` error log. Counters on `GET /api/v1/local-build/status`.
     `pkg/metrics` holds the Prometheus collectors. The EL RPC client tees
     every lifecycle transaction it sends into the pool while the pool is
     enabled (`execution.Client.SetTxTee`), so deposits land in pool-built
     blocks. `.hack/txgen` signs transfers into the pool and prints the hashes
     for an exact `build.local.queued` plan.
   - Per-slot: action plan `build.local` (`enabled`, `payload_source`, `tx_source`,
     `transactions`, `queued`, `build_el_payload` (the slot's engine build switch), `max_txs`, `gas_fill_pct`, `ordering`)
     resolves into `FrozenPlan.Build.Local` (rules carry it too). Slot results:
     each target's `testing` runs and pick changes (`selections`, see 4b). SSE:
     `testing_build` / `payload_selected` (slot-scoped), `txpool_stats` (on pool change), `service_status` gains
     `local_build_*`/`txpool_*`. WebUI: Mempool page, Testing builder and
     Mempool flow cards/drawer tabs, plan/rule editor section.

0e. **Inclusion lists** (FOCIL, EIP-7805, Heze) — `pkg/inclusionlist/` keeps the
   lists the beacon node gossips (`inclusion_list` SSE topic) keyed like the
   spec's `InclusionListStore` by (slot, dependent root): one list per
   committee member, equivocators ignored, timeliness from arrival against
   `INCLUSION_LIST_DUE_BPS`, pruned after 8 slots. `Store.View(slot, parent)`
   is the builder's view for a payload of slot+1 (only_timely=False): the
   unique transactions in committee order plus the committee bits
   (`chain.Service.GetInclusionListCommittee`, from the cached attester
   duties); several dependent roots are told apart by walking the parent
   chain. The builder service owns the store (`Service.InclusionLists()`).
   - Builds (`payload_builder/inclusionlist.go`): the view merged with the
     payload attributes' list, minus the plan's omissions, is recorded as
     `Payload.InclusionList` (`InclusionListBuild`). The engine build gets it
     in the forkchoiceUpdated V5 attributes (always present from Heze, empty
     if need be) and restarts with a new forkchoiceUpdated when a new list
     changes it during the wait (`restartWithInclusionList`; same deadline).
     Every testing build re-reads the view; a new list bumps the rebuild
     trigger. The pool selection (`txpool/inclusion.go`) places each list
     transaction that can execute on the parent state at the front of the
     block (sender nonce chain, base fee, balance, budgets; a nonce gap a
     pool transaction fills goes right after it); the others are recorded in
     `inclusion_list_skipped` (a list is satisfied when every missing
     transaction would be invalid appended to the block). Pool/fill
     transactions displaced at a list transaction's nonce count as
     `nonce_taken`, not stale. Refusals go through error attribution, list
     transactions included; only the exact sources (explicit, empty) carry
     the list verbatim and retry once without it.
   - Bids: `bid.inclusion_list_bits` per `build.inclusion_list_bits` —
     `view` (the lists observed at bid time, the honest builder; gossip
     ignores a bid missing a timely list a node saw, a proposer one missing a
     list it saw), `all`, `none` (`payload_bidder.InclusionListBits`, both
     the p2p and the Builder API paths).
   - Testing knobs: `build.inclusion_list_mode` include | omit (omit = a
     censoring payload) and the per-slot plan `build.inclusion_list
     {mode, bits, omit_txs}` (omit_txs censors exactly those transactions);
     the action plan grid draws an `IL` chip. Visible on slot results
     (the payload records' `inclusion_list`, the testing runs' `info`) and the
     slot graph's payload / testing build popovers

1. **Builder Service** (`pkg/payload_builder/`)
   - Main orchestrator for payload building
   - Subscribes to beacon node's `payload_attributes` events
   - Freezes the slot's action plan and acts on `frozen.Build`: decision, skip
     reason, build start time; reports `OnSlotBuilt` back for next_n accounting
     (plan-forced builds don't consume the budget)
   - Calls Engine API to construct execution payloads (forkchoiceUpdated → getPayload)
   - Fee recipients: the proposer's is resolved per build (gossip preferences
     post-Gloas, Builder API registrations pre-Gloas, else the
     `payload_attributes` suggestion) and lands on `Payload.ProposerFeeRecipient`
     (= `bid.fee_recipient`, where the CL pays the bid value). From Gloas the
     payload's coinbase (`Payload.FeeRecipient`) is buildoor's wallet, which
     collects the block value; before Gloas, or without a wallet, the payload
     pays the proposer directly
   - The payload cache (100 slots) releases a payload's blobs and proofs 8
     slots after its slot (`blobRetentionSlots`; unblinding and the reveal
     are due long before); the commitments stay. Read them through
     `BlobsBundle.Blobs()/Proofs()`; a released bundle fails an unblind or
     reveal loudly
   - **Builds, payloads, picks** — separate entities, never mixed. A
     BUILD TARGET is one (slot, parent tuple) (`BuildTargetEvent`, numbered by
     `Seq`); its BUILDS are the engine build (`EngineBuildEvent`) and the
     testing runs (`TestingBuildEvent`, `Run` 1..n); each build produces its
     OWN `Payload` (`Payload.Build`), completed (target identity, payload
     transform) before its build event and never modified afterwards; the
     PICK is the payload the cache hands to the consumers for the target,
     re-evaluated (`targetPicker`, `pick.go`) right after each build ends by
     the payload source's rule over the payloads that exist then — `el`:
     the engine payload; `local`: the best testing run, as soon as one
     exists; `higher_value`: moves only to a strictly more valuable block;
     `local_or_el`: the engine payload (fallback) until a testing run exists.
     A pick change is a `PayloadSelectedEvent` (block hash, `first` |
     `better`, fallback); a rebuild of the picked block (same hash) is the
     same payload, no change; a target whose builds all ended without a pick
     reports `no_payload`. Each consumer reads the pick of its own moment (p2p
     bid tick, Builder API request/response gate), so what a consumer USED is
     its own record — a bid's block hash (Builder API pre-Gloas header bids
     included), the reveal, the inclusion — shown per payload in the UI. All
     four events go out in order on ONE stream (`SubscribeBuildEvents`: a
     payload's build event precedes its pick, the superseded report ends a
     target). The `PayloadCache` holds every built payload once, keyed by
     block hash (`GetByBlockHash` finds any of them — unblind, reveal,
     inclusion), plus a separate target → picked hash map behind
     `Get`/`GetCandidate`/`GetVariant`/`GetSlotPayloads` (the bidders and
     the Builder API read the picks). A target's events go out under its
     own lock (`candidateBuild.emitMu`); plan-involved skips fire
     `BuildSkippedEvent` (deduped per slot) for the slot results tracker
   - **Missing-block fallback** (`applyAttributesFallback`): when no attributes
     arrived for a slot by its build start time, the previous slot's are
     re-used with the proposal slot advanced and the event marked
     `Synthesized`. Such attributes are stale whenever the state moved in
     between (an epoch transition changes the expected withdrawals), and Teku
     emits next-slot attributes only ~30 ms before the build start, so the
     fallback can win the race against a late node event. A node-received
     event for the same parent tuple therefore SUPERSEDES a build that ran
     from synthesized attributes (`supersedeSynthesizedBuild`): unless its
     build inputs are identical (`PayloadAttributesEvent.BuildInputsEqual`),
     the target's payloads and pick are withdrawn from the payload cache,
     a `BuildTargetEvent{superseded}` ends its events, its builds are
     cancelled and the tuple
     is rebuilt from the node's attributes via the late-build path

2. **Payload Bidder** (`pkg/payload_bidder/`) — shared Gloas+ bid/reveal domain
   - `Signer`, `BuildSignedBid`, `BuildSignedEnvelope`: bid/envelope construction + signing
   - `RevealService`: the ONLY envelope publisher. Own main loop (channel + timer +
     head-vote subscription, no polling); both flows request reveals via
     `RequestReveal`; dedupes per slot (exactly one publish per won slot). All
     settings come from the slot's frozen plan (`frozen.Reveal`, resolved from the
     standalone `reveal.*` config section — the reveal serves BOTH flows and is NOT
     an ePBS-bidder setting): gate mode `time` | `vote` | `vote_or_time` |
     `vote_and_time` (vote gates open event-driven when head-vote participation on
     the committing block reaches `reveal.vote_threshold_pct`; unsatisfied gates
     withhold at slot end with reason `vote_gate_timeout`), `broadcast_validation`
     passed to the envelope submission API (gossip | consensus |
     consensus_and_equivocation — the latter is the builder anti-unbundling
     protection), and the retry policy (`reveal.max_attempts` ×
     `reveal.retry_interval_ms`). Suppression: plan-disabled slots skip with
     `plan_disabled`, global `reveal.enabled=false` skips with `disabled` (a
     plan-custom slot force-activates either way and may bypass the in-slot
     deadline, clamped to slot end + 1 slot). Envelope construction is split from
     publish (`buildEnvelope`) so every per-attempt `RevealResult` carries the
     built envelope — failed publishes stay inspectable; envelope signing uses
     the TARGET slot's fork
   - `InclusionTracker`: own head-event loop; detects inclusion of our payloads (all
     forks), requests the p2p-side reveal, fires `PayloadIncludedEvent` (carries the
     `WonBlock` summary — storage is owned by the slot results tracker). Reorg-aware
     canonical verdicts (Gloas+): every won slot is re-evaluated against each new
     head's ancestry (parent-root walk over a cached block map, 16-slot window) and
     each verdict CHANGE fires `PayloadStatusEvent` (canonical | missed | orphaned) —
     recorded on the slot result's `inclusion.payload_status` (pending until the
     first follow-up block; the Action Plan cell renders it as the right dot)
   - `PaymentTracker`: pending payments + live balance adjustments (fed by
     InclusionTracker/RevealService; consumed by lifecycle and WebUI)
   - `ProposerPreferencesService`: caches Gloas gossip proposer preferences from the
     BN SSE stream in a `memstore.Store[Slot, *SignedProposerPreferences]`
     (first-per-slot, epoch-pruned, persisted via the `kv_store` namespace);
     implements `payload_builder.ProposerSettingsResolver`; the store is read
     directly by the p2p bidder (bid gate) and the builderapi epbs dialect (bids)

2b. **P2P Bidder** (`pkg/p2p_bidder/`) — active p2p bidding flow of ePBS only
   - Time-scheduled bidding (10ms tick), bid submission via gossip; each slot's
     window/interval/amounts come exclusively from the frozen plan snapshot
     (`frozen.Bid`, cached on the slot state) — the scheduler holds no config.
     A plan may activate bidding for a slot while ePBS is globally disabled and
     vice versa; `epbs_enabled`/`SetEnabled` are status reporting only
   - Custom plans support an absolute bid base (`bid_value_gwei`, allows
     underbidding) and a proposer-preferences-gate bypass (`ignore_missing_prefs`)
   - `CreateAndSubmitBid` returns the constructed signed bid even on gossip failure
     and signs with the target slot's fork; `BidSubmissionEvent` carries status,
     the signed bid, and the highest competitor bid (own index excluded)
   - Competitor bid tracking, registration state machine
   - Reveals/inclusion/payments are handled by the shared `payload_bidder` services

3. **Chain Service** (`pkg/chain/`)
   - Manages epoch-level beacon state
   - Reads the epoch state with go-eth2-client's `AgnosticBeaconState`
     (`*all.BeaconState`, decoded through dynamic-ssz's per-fork views) —
     never a hand-written SSZ decoder or stream walker: state decoding
     belongs to go-eth2-client/dynamic-ssz, a new fork's state layout is a
     library update. `GetValidatorPubkeyByIndex` serves the lookahead's
     proposers from memory and fetches any other index from the beacon node
     (bounded cache) — there is no all-validators pubkey array
   - Caches last 2 epochs of state
   - Detects fork transitions (Electra → Gloas)
   - Loads builder registrations from beacon state (post-Gloas)
   - Provides slot↔timestamp conversions over the **slot timeline**
     (`chain/slotclock.go`, EIP-8198): the slot duration is per fork —
     `SLOT_DURATION_MS` from genesis, `SLOT_DURATION_MS_<FORK>` (today
     `_HEZE`, listed in `slotDurationKeys`) from that fork's epoch; the
     deprecated `SLOT_DURATION_SCHEDULE` list is never read. `ParseChainSpec`
     pre-calculates the segments (`SlotSegment{Epoch, Slot, Offset,
     Duration}`) once, so `SlotStartOffset` / `SlotAtOffset` /
     `SlotDurationAt` are one pass over a handful of segments. Never compute
     slot times as `slot × duration` or `elapsed % duration`: go through
     `SlotToTime` / `TimeToSlot` / `SlotDurationAt(slot)` (BPS deadlines are
     shares of the slot's OWN duration). The segments reach the UI on
     `chain_info.slot_segments`, where `utils.ts` mirrors the lookups
   - `HeadVoteTracker`: per-slot attestation participation aggregated locally
     from raw `single_attestation` SSE events ONLY (streaming from the Gloas
     attester deadline at 25% of the slot). The aggregated `attestation` topic
     is deliberately NOT subscribed — aggregates arrive at the 50% aggregate
     deadline, which is also the `PAYLOAD_DUE_BPS` reveal deadline, too late
     to inform anything. Per-(slot, beacon_block_root) bitmaps over locally
     computed attester duties, O(1) merge with incremental balance accounting;
     undercounting is the only failure mode (the BN only emits singles for
     subscribed subnets — run it with subscribe-all-subnets). Updates are
     throttled (≥1 pp step per 100 ms flush per slot); crossing the
     configurable `epbs.head_vote_threshold_pct` (default 60 = the Gloas
     builder payment quorum, 0 = disabled) fires immediately with
     `threshold_met`. Feeds the WebUI `head_votes` SSE event (participation
     curve + threshold-met marker in the slot graph);
     `GetParticipation(slot, root)` exposes snapshots for future consumers
     (e.g. reveal gating). Per-vote arrival times are recorded (`voteTimes`,
     uint16 ms-offset per member) and served via `GetVoteDetail(slot, root)`
     (zero root = primary) together with the block ground-truth bitmap — the
     WebUI heatmap endpoint groups them by validator-ranges client name
     (retention 8 slots, in-memory only). **Sampled-subnet estimation**: a
     slot has at most one committee per attestation subnet, so a committee
     whose singles arrive densely (≥ min(16, half its size) distinct voters,
     any root) sits on a subnet the BN is subscribed to; stray votes from our
     own validators on other subnets never reach that bar. Once ≥64 members
     are sampled, participation is EXTRAPOLATED from the sampled committees
     (voted balance / sampled balance, `HeadVoteUpdate.Estimated`); fully
     sampled slots (subscribe-all-subnets) report the raw share unchanged. The
     graph, the reveal vote gate and the weak-head candidate check all consume
     this estimate. **Subnet coverage detection**: every imported block's
     attestations (fetched per head event, aggregate-format walk) are the
     ground truth compared against the singles bitmap, overall and within the
     sampled committees; a rolling 16-block window whose reliable view (raw
     when ≥80% seen, otherwise the sampled committees) sees <80% of its
     on-chain attesters (min 8 blocks / 16 attesters, recover at ≥90%) sets
     the `Low` flag on the `vote_coverage` SSE event (initial-state + on
     change) — the UI then shows a warning badge in the Slot Timeline header
     and hides the head-vote graph; an estimated-but-healthy view shows an
     "Estimated votes" info badge instead. Low coverage makes BOTH the graph
     AND vote-threshold-gated reveals unreliable (gates open late or never,
     withheld at slot end)

3b. **Builder Key Registry** (`pkg/builder_keys/`) — the managed set of builder
   BLS keys and the identity dependency of every module that used to hold a
   single `*signer.BLSSigner`.
   - **Derivation**: keys come from the operator's ENTRY key (`--builder-privkey`
     or `--builder-mnemonic` + `--builder-key-index`). Internal index 0 IS the
     entry key; index n ≥ 1 is `derive_child_SK(entry_sk, n)` — one EIP-2333 node
     deeper than any other participant's account path, which is what makes the
     set collision-free with other builders sharing a mnemonic. Derivation is
     cached for the process lifetime (`signer.DeriveInternalKey`).
   - **Two index spaces, never conflated**: `KeyIndex` is our derivation index
     (stable forever); `BuilderIndex` is the beacon registry index, assigned at
     deposit and REUSED by other builders after an exit.
   - **State** per key (`Status`: unused / depositing / pending / active /
     exiting / exited / withdrawn) is resolved from the epoch snapshot in ONE
     pass over `chainSvc.GetBuilders()`, so monitoring hundreds of keys costs the
     same beacon query as one. Only transactions scale with the fleet size.
   - **Usage history** (`kv_store` namespace `builder_keys`) survives restarts so
     a key deposited in an earlier run is recognised before the beacon state
     confirms it, and a key whose pubkey has left the registry is reused instead
     of pushing the highest index up forever. A persisted pubkey that disagrees
     with the derived one is a hard startup error (the entry key changed).
   - **Discovery** scans past the target for keys we used before and stops after
     `builder_keys.discovery_gap` never-used indices. Scanned-but-unused indices
     are derived, not tracked, so the fleet view is the size of the fleet.
   - **Selection** (`SelectForBid`) orders active keys per strategy
     (`round_robin` default, `single`, `random`, `least_used`) excluding keys
     already committed for the slot. Balance is a PREFERENCE, not a filter:
     underfunded keys sort last but are still offered when nothing else can
     cover the bid — deliberately underfunded bids are a scenario worth testing.

4. **Lifecycle Manager** (`pkg/lifecycle/`) — the fleet reconciler
   - Keeps the managed key count at `builder_keys.target_count`: deposits keys
     below it (`auto_deposit`), exits surplus keys above it (`auto_exit`,
     highest index first, skipping keys with pending payments the chain would
     silently ignore), and tops up whichever key fell below the threshold
   - At most ONE lifecycle transaction per reconcile pass: they all go through
     the single funding wallet and each waits for its receipt, so the fleet
     ramps instead of flooding the EIP-8282 deposit queue (whose fee grows with
     its length). `deposit_max_fee` then backs the ramp off on its own; a wallet
     that cannot cover the next deposit reports once instead of failing per key
   - Target changes wake the reconciler immediately (`Manager.Reconcile()`,
     wired from the settings `OnChange`)
   - Early onboarding covers the WHOLE target set: the pre-Gloas deposits sit in
     the pending queue together and the fork transition converts them all, so
     `pending_deposit_sim.go` asks whether the batch's FIRST entry survives
     (the queue drains from the front). The phase lasts until the fork: a key
     target raised after the first batch gets its own early batch (poke-woken)
   - Optional component (only active with `--lifecycle` flag)

4b. **Slot Results Tracker** (`pkg/slot_results/`) — generic per-slot outcome history
   - One attempt-aware `SlotResult` per slot where ePBS or the Builder API was active:
     build lifecycle (incl. `waiting_attributes`/`no_attributes` baselines from a slot
     clock so "planned but nothing happened" is visible), bid attempts (both
     transports, with statuses/competitor context/artifact refs), block submissions,
     reveal attempts, inclusion — plus the frozen `applied_plan` snapshotted at record
     creation. Copy-on-write records; attempts cap at 256/kind with a dropped counter;
     SSE updates coalesce per slot
   - Records older than two epochs keep only their summary in memory
     (`resident.go`); the full record is read back from the state-db for
     `Get`/`GetRange` and late updates, `GetSummaries` serves the grid's
     `view=summary` from memory. Swapped only once persisted
     (`memstore.ReplaceResident`); without a state-db records stay full
   - Consumes the services' BLOCKING subscriptions (loss-free history) and implements
     the `builderapi.SlotResultRecorder` interface for request-scoped recording
   - Builds: `SlotResult.build` holds the baseline status and every target
     (`targets`: engine build, testing runs, pick changes (`selections`) — each build names
     its payload by hash); `SlotResult.payloads` holds every payload built
     for the slot once, by block hash. Records of the old outcome model are
     mapped onto targets when read (`legacy.go`)
   - `ArtifactStore`: raw SSZ artifacts per slot — payloads keyed by block
     hash (the engine payload and every picked one), every signed bid
     (restart-safe per-slot indices via `MAX(idx)+1`), the signed envelope at
     construction time — write-through to a bounded 64-slot memory buffer (works
     without a state-db) + async batching writer into the `slot_artifacts` table;
     hot paths never wait on SQLite; toggled by `slot-artifact-capture-enabled`
   - Serves the Bids Won view (unchanged wire shape) as a filtered included-slot view;
     migrates the legacy `won_blocks` kv namespace once (merge-safe, idempotent,
     crash-safe); prunes summaries to `slot-result-retention-epochs` and artifacts to
     `slot-artifact-retention-epochs` (both default 100) on epoch transitions

5. **Builder API Server** (`pkg/builderapi/`) — thin host + two dialect subpackages
   - `builderapi/legacy/`: pre-Gloas dialect (Electra/Fulu via agnostic types) —
     registerValidators, getHeader, submitBlindedBlockV2 (unblind + publish)
   - `builderapi/epbs/`: post-Gloas dialect (Gloas/Heze+) — getExecutionPayloadBid,
     submitSignedBeaconBlock (broadcasts the block immediately, then hands the reveal to
     `payload_bidder.RevealService` — no inline envelope publish), submitBuilderPreferences
   - **Bid serving is decided exclusively by the slot's frozen plan** (resolved at
     request time): suppressed slots 204; plan-activated slots serve even when the
     module is globally disabled.
   - **Response gate** (timing game, `builder_api.respond_at_ms`, slot-relative
     ms, 0 = respond immediately, per-slot plan override `respond_at_ms`): both
     dialects hold a validated request until the gate
     (`ResolvedBuilderAPISettings.AwaitRespondGate`) and only then pick the
     payload, so the response carries the best payload built by then
     (the target's pick at the gate); a hang-up during the hold records
     `cancelled`. Distinct from the plan's `response_delay_ms`, a testing knob
     applied after the bid is built. Drawn as the "respond" gate in the slot
     graph's Builder API row The `enabled` flag keeps gating only the
     non-slot-scoped endpoints (registrations, preferences, block submission).
     Frozen per-slot values drive subsidy, absolute total bid value (uint256 wei
     math), the execution-payment split and a context-cancellable response delay.
     Bid requests beyond `currentSlot+1` are rejected with 400 before freezing.
     The Gloas execution-payment split (`builder_api.execution_payment_gwei`
     absolute, wins over `builder_api.execution_payment_percent`; default 0 =
     everything paid trustlessly via bid value) claims part of the served total
     as `execution_payment` — a deliberately UNBACKED claim (no EL payment tx is
     ever made), capped by the proposer's advertised `max_execution_payment`
     preference (0 when never submitted, per spec) unless
     `builder_api.ignore_preference_limit` deliberately serves beyond it (a
     spec-violating bid clients should reject); gossip bids stay
     `execution_payment = 0` per spec. `auth.message.data` must be the
     hostname of `--builder-api-url` (the builder-specs default, see
     `epbs/auth_data.go`) or, for validator clients predating that default,
     the URL bytes verbatim. An unset `--builder-api-url` skips the match on
     BOTH ePBS handlers (bids and preferences alike)
   - Outcomes are recorded through the narrow `SlotResultRecorder` interface
     (implemented by the slot results tracker): bids `served` only after a
     successful response write, `suppressed`/`failed`/`cancelled` otherwise, with
     the exact signed object for artifact capture and polling dedupe; block
     submissions record `received`/`accepted`/`failed` on all submit paths
   - Parent `Server`: route table, shared stores, stats aggregation, enable fan-out,
     debug endpoints; no won-block tracking here — the slot results tracker owns
     outcome records (inclusion-time semantics)
   - Validator fee recipient management: registrations live in a
     `memstore.Store[BLSPubKey, *SignedValidatorRegistration]` created in `cmd/run.go`
     (persisted via the `kv_store` `validator_registrations` namespace) and feed the
     pre-Gloas `legacy.RegistrationSettingsResolver`; builder preferences
     (max_execution_payment) are memstore-backed too and survive restarts

6. **WebUI** (`pkg/webui/`)
   - React/TypeScript app served publicly: everything is readable, actions
     (switches, edits, key actions) and private data (pool contents, custom
     payloads, callers/keys, explicit plan transactions, the audit log) need a
     login. Two views: **Overview** (the landing page at `/`, end users: what
     the builder does, blocks won, last 16 slots, how to send private
     transactions and register validators) and **Operator** (`/dashboard` and
     every other page, reached through the header switch)
   - ONE SSE connection per tab (`EventStreamProvider` in
     `hooks/useEventStream.ts`, read with `useEventStream()`), so page switches
     neither reconnect nor replay; per-slot state is kept for the last
     `SLOT_HISTORY` (100) slots
   - **Dashboard**: stats row, the slot timeline (a scrollable 100-slot window
     that renders only visible slots; follows new slots while scrolled to the
     top) beside the Builder and Next slot (`slot_outlook`) cards, the event log
     folded into one line, and one card per flow (`settings/flows.ts`: Engine
     builder, Testing builder, Keys & candidates, Lifecycle, ePBS bidder,
     Builder API, Payload reveal, Mempool /rpc) with its one-click switch and
     its configuration read-only (`cli`/`ui` tags from `setting_sources`)
   - **Settings drawer** (`components/settings/SettingsDrawer.tsx`, opened with
     `openSettings(flow, tab)` from any page) is the ONLY settings editor. It
     edits a snapshot: live updates follow untouched fields and turn edited ones
     into explicit conflicts (never overwrite a field being typed in); Save
     posts only the changed keys; irreversible changes (lowering the key
     target, auto exit) need a confirmation. Fields come from the registry in
     `settings/fields.tsx` (one entry per backend setting key: label, kind,
     unit/scale, options, validation, help text) and are written through
     `settings/write.ts` (generic settings endpoint, key target)
   - Mempool page (tabs Next block / Public mempool / Private mempool /
     Access / Settings; old sub paths `queue`, `el-mempool` still route).
     Next block = two cards: the current slot's pick (live from the build
     events; split by origin from the testing run's selection summary
     `origins`) and the next slot's estimate (`GET /api/v1/next-block`): a
     gas bar and blob slots by origin (inclusion list / private / public),
     key stats, skips, and for logged-in readers a block map (width = gas,
     height = effective tip) and the transaction list. Public mempool = the
     mirror's state, a pending-gas-by-max-fee chart against
     the next base fee and one block of gas, buildoor's private txs
     highlighted for logged-in readers, and the mirrored txs) and one tabbed
     Registrations page (Builder API registrations, proposer preferences,
     payment caps; old /validators, /proposer-preferences,
     /builder-preferences paths redirect)
   - Design tokens + shared primitives in `src/styles/tokens.css` and
     `components/ui/` (light and dark theme); self-hosted IBM Plex fonts
   - **Action Plan View**: epoch × slot timetable (rows = epochs, columns =
     slots) rendering plan chips + result status per cell (from the compact
     `view=summary` results; a slot opened in detail loads its full record);
     a side drawer (`actionplan/SlotEditDrawer.tsx`, tabbed like the
     dashboard flows: Engine builder, Testing builder, ePBS bidder, Builder
     API, Payload reveal, Advanced) edits future slots and shows the full
     outcome + SSZ/JSON artifact downloads of past slots. Selection: click
     selects + opens, shift-click selects the range from the anchor,
     Ctrl/Cmd-click toggles; unsaved edits ask before the drawer retargets.
     A Recurring Rules panel
     (list/toggle/edit/delete + scenario presets like "win the last slot of
     every epoch and withhold the payload"); rule-derived cells render dashed
     chips; live via the `action_plan_updated`/`action_plan_rules_updated`/
     `slot_result_updated` SSE events
   - **Bids Won View**: Paginated table of our blocks included on chain
     - Real-time updates when new blocks are included (bid_won SSE from the
       inclusion tracker's `PayloadIncludedEvent`)
     - Click-to-copy block hashes, relative timestamps
     - Shows: slot, block hash, # transactions, # blobs, value in ETH

### Event Flow

```
Beacon Node ─payload_attributes─▶ payload_builder ─builds+selection (cache)─▶ p2p_bidder (bids via gossip)
Beacon Node ─head events────────▶ payload_bidder.InclusionTracker ─▶ RevealService ─▶ Beacon Node
Proposer ───Builder API─────────▶ builderapi (legacy: unblind+publish │ epbs: block broadcast
                                              + RevealService.RequestReveal)
```

### Data Flow (ePBS Mode)

1. Beacon node emits `payload_attributes` event
2. Builder validates slot against schedule and builds at `BuildStartTime`
   (Engine API: forkchoiceUpdated → getPayload, plus testing runs), reports
   each build's payload and selects the one feeding the bids
3. p2p_bidder scheduler ticks every 10ms and submits bids inside the slot's FROZEN
   bid window (per-slot plan > global config; gated on builder registration and, per
   slot, on the frozen enable resolution)
4. Head event → InclusionTracker matches the block against our payload cache; on a win
   it records the pending payment, requests the reveal, and fires the inclusion event
   (the slot results tracker stores the outcome)
5. RevealService publishes the envelope once the slot's frozen reveal gates open
   (time and/or vote threshold), deduped per slot — a Builder-API-won block's reveal
   was already requested by the epbs dialect handler at block submission, so the
   p2p-side request is a no-op. Plan-suppressed slots skip with reason
   `plan_disabled`, globally disabled reveals with `disabled`, unsatisfied vote
   gates with `vote_gate_timeout`
6. RevealResult → payment moves from pending to balance deduction; WebUI event fires;
   the slot results tracker records the attempt + envelope artifact

### Fork Compatibility

- **Electra/Fulu**: Payload included in block, built on parent
- **Gloas**: Payload revealed separately after block production
- PayloadBuilder handles fork-specific differences automatically
- Chain Service detects fork transitions via epoch state

### Configuration System

Configuration is managed via:
- CLI flags (highest priority)
- YAML config file (`--config` flag or `./buildoor.yaml`): a FLAT map keyed by
  flag name without the dashes (`deposit-amount: ...`), not the nested
  `yaml` struct tags; unknown keys fail the start (`checkConfigFileKeys`)
- Environment variables: `BUILDOOR_<FLAG_NAME>` with dashes as underscores
  (`BUILDOOR_DEPOSIT_AMOUNT`)

Key config sections:
- **Builder keys**: `--builder-privkey` (BLS) or `--builder-mnemonic` +
  `--builder-key-index` (the ENTRY key), `--wallet-privkey` (ECDSA)
- **Managed key set**: `--builder-keys-target` (keys kept registered and
  funded; default 1 = the entry key alone, byte-identical to single-key
  behaviour), `--builder-keys-max-index` (derivation cap, default 1000),
  `--builder-keys-discovery-gap` (unused indices ending the startup scan,
  default 100), `--builder-keys-auto-deposit` / `--builder-keys-auto-exit`
  (both default true; auto-exit is irreversible — an exited key cannot be
  reactivated). All but the discovery gap are mutable via
  `builder_keys.*` settings keys
- **Key selection**: `--epbs-key-strategy` (round_robin | single | random |
  least_used), `--epbs-bid-keys-per-slot` (cap per slot, 0 = no cap beyond the
  fleet), `--epbs-bid-keys-per-step` (keys bidding a payload per interval step,
  each one increment higher; 1 = walk the fleet up the ladder, 0 = spend every
  remaining key at once), `--builder-api-key-strategy` (empty = follow the ePBS
  strategy). A key is SPENT once one of its bids reaches the network — the
  gossip rules ignore a builder's later bids for a slot — so every bid, an
  escalated re-bid of the same payload included, takes a key that has not bid
  yet, and the submissions of one step go out concurrently
- **Lifecycle amounts**: `--deposit-amount` (gwei; the ONE amount sent with
  every builder key deposit — early onboarding via the regular deposit
  contract, the registering builder deposit, and each top-up; there is
  deliberately no separate top-up amount), `--topup-threshold` (effective
  balance below which a key is topped up), `--deposit-max-fee` (deposits and
  top-ups wait while the queue fee is above it), `--early-deposit-tip`
  (priority fee floor of the early onboarding deposits, default 50 gwei, max
  10000, 0 = EL suggestion). All four are mutable settings edited in the
  Lifecycle drawer tab
- **Clients**: `--cl-client`, `--el-engine-api`, `--el-rpc`, `--el-ws`
  (optional websocket: the EL mempool mirror follows arrivals live)
- **Schedule**: `--schedule-mode` (all/every_nth/next_n), `--schedule-every-nth`, `--schedule-next-n`
- **Inclusion lists** (Heze): `--build-inclusion-list-mode` (include | omit),
  `--build-inclusion-list-bits` (view | all | none); mutable via the
  `build.inclusion_list_*` settings keys (Engine builder drawer tab)
- **ePBS timing**: `--build-start-time`, `--epbs-bid-start`, `--epbs-bid-end`
- **Bidding**: `--epbs-bid-min`, `--epbs-bid-increase`, `--epbs-bid-interval`,
  `--epbs-bid-subsidy` / `--builder-api-subsidy` (SIGNED gwei: positive pads
  the bid over the block value, negative is the builder's margin kept from it
  — p2p `max(blockValue + subsidy, bid_min)`, Gloas Builder API floored at 0,
  pre-Gloas getHeader ignores a negative value because its payload pays the
  proposer directly; shared math in `payload_bidder.ApplySubsidyGwei`),
  `--epbs-bid-value-override` (absolute p2p bid base, 0 = off),
  `--epbs-vote-threshold` (head-vote participation threshold in percent,
  default 60, 0 = off),
  `--builder-api-value-override` (absolute served total value, 0 = off),
  `--builder-api-execution-payment` / `--builder-api-execution-payment-percent`
  (unbacked `execution_payment` portion of served Gloas bids, absolute gwei
  wins over percent, 0 = off; capped by the proposer's advertised
  `max_execution_payment` unless `--builder-api-ignore-preference-limit`)
- **Payload reveal** (own section — serves both the p2p bidder and Builder
  API flows): `--reveal-enabled` (default true), `--reveal-gate-mode`
  (time | vote | vote_or_time | vote_and_time, default vote_or_time —
  reveal at the payment quorum or the time gate, whichever first),
  `--reveal-time` (time gate in ms, 0 = auto), `--reveal-vote-threshold`
  (participation % opening the vote gate, default 60),
  `--reveal-broadcast-validation` (gossip | consensus |
  consensus_and_equivocation, default consensus_and_equivocation — the
  builder anti-unbundling protection), `--reveal-max-attempts` (default 3),
  `--reveal-retry-interval` (default 500 ms). All mutable via
  `PATCH /api/v1/config` with `reveal.*` keys; per-slot overridable
  through the action plan's reveal category (gate_mode, vote_threshold_pct,
  broadcast_validation, reveal_time_ms)
- **Slot history**: `--slot-result-retention-epochs` (default 100),
  `--slot-artifact-retention-epochs` (default 100; raw payloads dominate disk),
  `--slot-artifact-capture-enabled` (default true)
- **Local build / tx pool**: `--local-build-enabled`,
  `--local-build-start-time` / `--local-build-end-time` (the testing build
  window, signed ms relative to the slot start, 0 = auto as a share of the
  slot), `--local-build-rebuild-interval` (min gap between change-triggered
  testing rebuilds, 0 = one build timed to the window end; see "Testing
  build timing" in 0d),
  `--local-build-payload-source`
  (el | local | local_or_el | higher_value), `--local-build-tx-source` (txpool | private_first | most_profitable | empty |
  el_mempool), `--local-build-allow-blobs-without-bundle`,
  `--local-build-blob-encoding` (startup-only); `--txpool-enabled`,
  `--txpool-auth` (open | api_key), `--txpool-auth-token` (static API key, startup-only),
  `--txpool-store` (pool persistence directory, startup-only, empty = memory),
  `--txpool-rpc-anon-rate`, `--txpool-rpc-anon-scope`, `--txpool-rpc-key-rate`,
  `--txpool-rpc-proxy-hops`, `--txpool-rpc-upstreams`, `--txpool-ordering`, `--txpool-block-max-txs`,
  `--txpool-gas-fill-pct`, `--txpool-max-txs`, `--txpool-max-txs-per-sender`,
  `--txpool-tx-ttl-slots`, `--txpool-forward-to-el`. Both require `--el-rpc`;
  mutable via `local_build.*` / `txpool.*` settings keys
- **State persistence**: `--state-db <path>` (optional SQLite; see below)

### Settings Service & State Persistence (`--state-db`)

The **Settings Service** (`pkg/settings`) is the central authority for all *mutable*
runtime config. It owns the single effective `config.Config` that every module reads
and is the only writer. Setting values resolve from three layers:

```
hardcoded defaults  <  CLI-supplied (flag/env/config)  <  UI override
```

CLI vs UI is resolved by **recency** (a monotonic seq), not fixed priority:
- A CLI value that *changed* since the last run wins over an older UI override.
- An *unchanged* CLI flag lets a newer UI override win.
- A CLI "change" is detected by diffing the operator-supplied value against the
  last-seen one persisted in the state-db. Only keys where `viper.IsSet` is true
  form the CLI layer, so bumping a *hardcoded default* in a new release never
  clobbers a UI override.

**Every registered setting must be editable from the WebUI.** Every key needs an
entry in the frontend field registry (`pkg/webui/src/settings/fields.tsx`) and a
place in a flow's drawer tabs (`settings/flows.ts`); the drawer writes it by its
canonical registry key through `PATCH /api/v1/config`, and
`pkg/webui/settings_coverage_test.go` fails when a registry key has no editor — never add a setting that is only reachable by CLI
flag, and never add a second setting for something an existing one already
means (deposits and top-ups share the single `deposit_amount`). Operator values
from BOTH sources pass `config.ValidateSetting` (CLI at startup, UI at write
time): modules fall back to a default for values they do not recognise, so an
unvalidated typo would be stored, displayed and ignored. A setting that is
renamed or merged is listed in `retiredKeys` (`pkg/config/settings.go`) so its
persisted UI override is adopted by the successor instead of being dropped.

**Slot duration changes** (`config/slot_timing.go`): the millisecond timings
positioned in or sized to the slot (`slotScaledKeys`: build start, payload
build time, bid window + interval, reveal time, testing build window +
rebuild interval, speculative build time, respond-at) are written for one slot
duration. When the chain reaches a new duration they are patched ONCE:
`RescaleSlotTiming` recomputes the defaults (`ApplySlotDefaults`) and writes
every operator value (CLI or UI) scaled by new/old as a UI override (actor
`slot-duration-change`, so an unchanged CLI flag keeps the scaled value on
restart; a changed flag counts as written for the current duration). The
duration the settings are scaled for is the reserved `_slot_timing` settings
row (absent: the genesis duration). The plan service triggers the patch right
before the first slot of a new segment freezes (`PlanService.Freeze` →
`rescaleSlotTiming`), so the last slot of the old duration keeps its values
and the new slot's build/bid window (which starts in the slot before) already
uses the new ones; startup patches a change that passed while buildoor was
down. Plans and rules are NOT rescaled: their values are taken as written for
their slots (rules validate against every duration within their epoch window).

The mutable-setting registry lives in `pkg/config/settings_fields.go` (keys in
`settings_keys.go`);
the per-module enable flags (`epbs_enabled`, `builder_api_enabled`,
`lifecycle_enabled`) are ordinary settings too. Write handlers call
`settingsSvc.SetMany`, which mutates the shared config in place, persists overrides,
and fires `OnChange` callbacks (registered in `cmd/run.go`) that trigger module
resets (`builder.UpdateConfig`, `epbs.UpdateConfig`) and `SetEnabled` syncs.

The optional **state-db** (`pkg/db`, mirrors spamoor: `glebarez/go-sqlite` + `sqlx`
+ goose migrations) persists across restarts when `--state-db <path>` is set:
- `settings` — the 3-way (cli/ui + seq) override state
- `kv_store` — generic namespaced key/value blobs behind `memstore` persistence
  (buffered write-behind). Namespaces: `proposer_preferences` (Gloas gossip prefs,
  SSZ), `validator_registrations` (Builder API registrations, SSZ; codec in
  `builderapi/legacy`), `builder_preferences` (max_execution_payment, LE uint64;
  codec in `builderapi/epbs`), `slot_plans` (per-slot action plans, JSON; codec in
  `action_plan`), `slot_rules` (recurring per-slot-index plan templates, JSON;
  codec in `action_plan`), `slot_results` (per-slot outcome summaries, JSON; codec in
  `slot_results`; the legacy `won_blocks` namespace is migrated into it once on
  startup and then deleted)
- `slot_artifacts` — dedicated table (NOT a kv namespace: blobs are too large for
  the in-RAM memstore pattern and pruning needs a SQL range delete on the integer
  slot column) holding raw SSZ artifacts per slot (payload/bid/envelope), fed by
  the tracker's async batching writer. SQLite does not shrink the file on delete
- `audit_log` — every authenticated mutating action (actor from JWT subject)

Pool transactions are deliberately NOT in the state-db: `--txpool-store` gives
the owned pool its own pebble database (`txpool/store.go`, go-ethereum's
engine, LSM for the per-slot churn; RLP records keyed by tx hash with
submitter/arrival/seq; puts and deletes coalesced in memory and flushed
every 100 ms without fsync, synced on Stop; `Start` re-admits the records,
TTL and stale-nonce checks apply as usual).

When `--state-db` is unset the database runs in a disabled no-op mode and behaviour
is in-memory-only as before. Repository methods early-return; never nil-check the
`*db.Database` at call sites.

### Startup Sequence

The application initializes services in this order (see `cmd/run.go`; the
numbered step comments there match this list 1:1):
1. Initialize CL client
2. Initialize Engine API client
3. Initialize BLS signer
4. Initialize RPC client and wallet (if lifecycle available)
5. Fetch chain spec & genesis (wait for the beacon node), apply slot-time timing defaults
6. Open the state-db (`--state-db`) and initialize the central Settings Service (applies persisted overrides into `cfg` in place before any module reads it)
7. Start chain service
7b. Start the action plan service (the per-slot scheduling authority; persisted via the `kv_store` `slot_plans` namespace; a mandatory constructor dependency of every action module below)
8. Initialize lifecycle manager (if prerequisites available)
9. Initialize builder service (when Builder API is available, also creates the validator registration memstore — persisted via `kv_store` — and registers the pre-Gloas `legacy.RegistrationSettingsResolver`)
9b. Start shared payment tracker + reveal service (Gloas scheduled) and inclusion tracker (always) from `pkg/payload_bidder`
10. Initialize proposer preferences service (if Gloas fork is scheduled; registers the payload builder's Gloas+ settings resolver, store persisted via `kv_store`)
11. Initialize p2p bidder service (if Gloas fork is scheduled; bid-gates on the proposer preferences store)
12. Initialize Builder API server (if `--api-port` set; epbs dialect reads the proposer preferences store; builder preferences persisted via `kv_store`)
12b. Start the slot results tracker (before the producer services so its blocking subscriptions never miss an event; runs the `won_blocks` migration; registers as the Builder API's result recorder)
13. Initialize and start validator ranges resolver
14. Register settings `OnChange` subscribers (push changes to modules; schedule changes reset the plan service's next_n accounting)
15. Start WebUI/API server (if APIPort > 0)
16. Wire lifecycle manager callbacks to ePBS (if both present)
17. Start builder service
18. Start p2p bidder service (if available)
19. Start lifecycle manager (uses the shared payment tracker from step 9b)
20. Start proposer preferences service (if initialized)
21. Wait for shutdown signal

### RPC Clients

- **Beacon Client** (`pkg/rpc/beacon/`): Uses attestantio/go-eth2-client for event streaming (head, payload_attributes, bids, payload_envelopes) and API calls
- **Engine Client** (`pkg/rpc/engine/`): JSON-RPC to execution layer Engine API
- **Execution Client** (`pkg/rpc/execution/`): Standard EL JSON-RPC for wallet interactions

### Important Patterns

1. **Event-Driven Architecture**: Services communicate via event subscriptions, not direct calls
2. **Dispatcher Pattern** (`pkg/utils/Dispatcher`): Generic pub-sub for internal events
3. **Time-Based Scheduling**: ePBS uses precise timing relative to slot boundaries, not just event triggers
4. **Fork Awareness**: All payload building logic checks current fork and adjusts behavior
5. **Subscription Model**: Builder doesn't know about ePBS; ePBS subscribes to Builder's events
6. **No function pointers as struct fields / constructor params**: Don't store callbacks like `func(slot) bool` on a struct or thread them through constructors — they are hard to read and obscure what a type actually depends on. Pass the concrete dependency (the struct that owns the behavior, e.g. a `*memstore.Store[...]`) and call its method directly. Dispatcher subscriptions (pattern 1/2) are the sanctioned way to decouple; ad-hoc callbacks are not.
7. **Always hash tree roots via dynssz**: To compute any SSZ hash tree root, use `dynssz.GetGlobalDynSsz().HashTreeRoot(obj)` (`dynssz "github.com/pk910/dynamic-ssz"`), never the type's statically generated `obj.HashTreeRoot()`. The generated method hardcodes mainnet list limits, so it produces wrong roots under the minimal preset; the global dynssz resolves preset-dependent limits from the active spec. See `pkg/payload_bidder/bid.go`.

## Code Structure

```
buildoor/
├── cmd/                    # CLI commands (root, run, deposit, exit)
├── pkg/
│   ├── action_plan/       # per-slot scheduling authority: sparse SlotPlan store,
│   │                      # recurring SlotRules (slot-index-in-epoch templates,
│   │                      # wholesale precedence under explicit plans),
│   │                      # freeze semantics (FrozenPlan = raw plan + resolved
│   │                      # effective settings + complete build decision), atomic
│   │                      # bulk updates w/ path-based partial edits, kv codecs
│   ├── slot_results/      # generic per-slot outcome history: attempt-aware
│   │                      # SlotResult tracker (blocking subscriptions, baseline
│   │                      # slot clock, won-block view, won_blocks migration) +
│   │                      # ArtifactStore (raw SSZ payload/bids/envelope, async
│   │                      # batched writer into the slot_artifacts table)
│   ├── txpool/            # owned transaction pool (stateless admission, state-aware
│   │   │                  # selection, head-driven eviction) + rpc/ JSON-RPC ingress
│   ├── builder/           # Core payload building logic
│   ├── builderapi/        # Builder API host (route table, shared stores, stats)
│   │   ├── legacy/        # pre-Gloas dialect (Electra/Fulu): registerValidators,
│   │   │                  # getHeader, submitBlindedBlockV2, bid build/unblind helpers,
│   │   │                  # registration signature verify + kv_store codec + pre-Gloas
│   │   │                  # settings resolver (registration store itself is a
│   │   │                  # memstore instance created in cmd/run.go)
│   │   └── epbs/          # post-Gloas dialect (Gloas/Heze+): payload bid, beacon block
│   │                      # (block broadcast + scheduled reveal), builder preferences
│   │                      # (memstore-backed, persisted via kv_store), request auth
│   │                      # + SSZ types
│   ├── chain/             # Beacon state management
│   ├── config/            # Configuration types and defaults
│   ├── db/                # Optional SQLite state-db (settings, kv_store, audit, ...)
│   │   ├── database.go    # Database struct, Init, migrations, disabled no-op mode
│   │   └── schema/        # Embedded goose migrations
│   ├── settings/          # Central settings service (3-way default<cli<ui resolution)
│   ├── p2p_bidder/        # active p2p bidding flow of ePBS (bid windows, competitor
│   │                      # tracking, registration state) — no reveal/payment logic
│   ├── memstore/          # generic thread-safe keyed store w/ buffered persistence
│   ├── lifecycle/         # Deposit/exit/balance management
│   ├── payload_bidder/    # shared Gloas+ domain: Signer, bid/envelope build,
│   │                      # RevealService (plan-aware timing/suppression),
│   │                      # InclusionTracker (detection + events; storage in
│   │                      # slot_results), PaymentTracker,
│   │                      # ProposerPreferencesService (gossip prefs store + resolver)
│   ├── rpc/
│   │   ├── beacon/        # Beacon node client
│   │   ├── engine/        # Engine API client
│   │   └── execution/     # Execution RPC client
│   ├── signer/            # BLS signing utilities
│   ├── utils/             # Shared utilities (Dispatcher, etc.)
│   ├── wallet/            # ECDSA wallet for transactions
│   └── webui/             # HTTP server and React frontend
│       ├── handlers/      # HTTP API handlers
│       │   └── api/
│       │       ├── api.go     # REST endpoints (includes GetBidsWon)
│       │       └── events.go  # SSE event streaming (includes bid_won)
│       ├── src/           # React TypeScript source
│       │   ├── components/
│       │   │   ├── BidsWonView.tsx      # Bids Won page container
│       │   │   ├── BidsWonTable.tsx     # Table with pagination
│       │   │   ├── Pagination.tsx       # Reusable pagination component
│       │   │   └── ...
│       │   ├── hooks/
│       │   │   ├── useBidsWon.ts        # Data fetching for bids won
│       │   │   └── useEventStream.ts    # SSE connection (handles bid_won)
│       │   └── types.ts       # TypeScript interfaces (includes BidWonEntry)
│       └── static/        # Bundled assets
├── contracts/             # Solidity contract ABIs
├── bin/                   # Build output
├── Makefile              # Build commands
├── Dockerfile            # Container image
└── main.go               # Entry point
```

## Development Notes

### Module Replacement
The `go.mod` file includes a critical replace directive for ePBS/Gloas types:
```go
replace github.com/attestantio/go-eth2-client => github.com/pk910/go-eth2-client v0.0.0-20260109010443-3742e71092e1
```
This fork includes ePBS-specific data structures not yet in upstream.

### Adding New Features

When adding features that interact with the builder:
1. Subscribe to events via `builder.Service.Subscribe()` rather than calling methods directly
2. Use the Dispatcher pattern for internal event distribution
3. Check fork compatibility in `chain.Service.GetForkName()`
4. Consider time-based scheduling for slot-relative operations

### WebUI Development

Frontend is a React/TypeScript app using Webpack:
- Entry point: `pkg/webui/src/index.tsx`
- Real-time updates via SSE at `/api/v1/events`
- Custom hook `useEventStream()` manages app state from events
- Backend serves static files via `pkg/webui/static/`
- **Navigation**: `stores/viewStore.ts` maps paths to views (sub paths like
  `/mempool/access` belong to their view), conditional rendering without React
  Router
- **Styling**: Bootstrap 5 CSS with custom components

To make frontend changes:
1. `cd pkg/webui && npm run dev` (watch mode)
2. Backend serves from `static/` automatically
3. Rebuild binary only if adding new API endpoints

#### WebUI API Endpoints

**Path layout** (`pkg/webui/routes.go`, one table): buildoor's own REST API
lives under `/api/v1` (`APIBasePath`; the frontend builds every URL through
`src/utils/api.ts` `apiPath`), its Swagger UI at `/api/docs/` (`@BasePath
/api/v1`). The spec-owned and ops paths keep their own: the Builder API
(`/eth/v1/builder/*`, `/eth/v2/builder/blinded_blocks`), the JSON-RPC
ingress (`/rpc`), `/metrics`, `/debug/pprof/*`. Every pre-v1 path (`/api/...`,
`/api/buildoor/...`) of a v1 route stays served as an alias until 2.0, with
`Deprecation: true` and a `Link: <successor>; rel="successor-version"`
header. Nothing under `/api/` falls through to the SPA: an unmatched path is
a JSON 404, a wrong method a JSON 405 with `Allow` (computed by
`apiFallback`, as gorilla/mux loses method mismatches inside subrouters).
Settings, the module enable flags included, are written with one
`PATCH /api/v1/config`; key lifecycle actions go through
`/api/v1/builder-keys/{index}/…` (index 0 = the entry key). The `overview`
command polls `/api/v1/overview` and falls back to the pre-v1
`/api/buildoor/overview` of older instances.

**Buildoor-specific endpoints:**
- `GET /api/v1/registrations/validators` - List registered validators
- `GET /api/v1/bids-won` - Paginated list of won blocks (read from the slot
  results tracker's included-slot view; Builder API and p2p ePBS wins alike)
  - Query params: `offset` (default: 0), `limit` (default: 20, max: 100)
  - Returns: `{ bids_won: [], total: number, offset: number, limit: number }`
- `GET /api/v1/builder-api/status` - Builder API configuration and validator count
- `GET /api/v1/action-plan?min_slot=&max_slot=` - EFFECTIVE per-slot action
  plans in the inclusive range (max span 320 epochs): stored plans plus the
  rule-derived plans of the still-open slots (those carry `rule_id`)
- `POST /api/v1/action-plan` - Atomic bulk plan mutation (auth + audit).
  Body `{updates: [...]}`; each update targets `slots` and/or `from_slot..to_slot`,
  with three-state category members (absent/null/object) and fine-grained `set`
  paths (`{"bid.bid_min_amount": 5000}`) plus the top-level `ignore_rules`
  member (per-slot recurring-rule opt-out). Past/frozen slots → 409. Returns the
  authoritative normalized plans
- `GET /api/v1/action-plan/rules` - Recurring rules, id-ascending (= match
  order)
- `POST /api/v1/action-plan/rules` - Atomically replaces the WHOLE rule set
  (auth + audit). Body `{rules: [...]}`; each rule has an id slug, `enabled`,
  `slots_in_epoch`, optional `from_epoch`/`to_epoch` and the same categories as a
  slot plan. All-or-nothing validation (400 on any bad rule); max 32 rules
- `GET /api/v1/slot-results?min_slot=&max_slot=&view=` - Attempt-level outcome
  history per slot (build, bids, submissions, reveals, inclusion, applied plan);
  `view=summary` trims each record to what overview grids need (no applied
  plan, attributes, testing build runs or per-candidate builds)
- `GET /api/v1/slot-results/{slot}/payloads/{blockHash}|payload|envelope|bids|bids/{index}` - Raw
  SSZ artifacts (`payloads/{blockHash}`: any captured payload of the slot;
  `payload`: the primary target's current pick, `?candidate=` another
  target's; a testing payload, and the envelope revealing one, need a login) with beacon-API content negotiation: `Accept:
  application/octet-stream` → exact SSZ bytes, otherwise `{"version", "data"}`
  JSON; responses carry `Eth-Consensus-Version` + `Vary: Accept`; the bid listing
  is JSON-only metadata
- `GET /api/v1/local-build/status` - Probed `testing_buildBlockV1`
  availability (+ per-EL enable hint, blob handling), effective local build
  settings, pool availability/toggle. `POST /api/v1/local-build/probe`
  re-probes now (auth + audit)
- `GET /api/v1/txpool?offset=&limit=&sender=&sort=` - Pool stats (pending,
  senders, gas sum, value, blobs, lifetime counters) + a page of queued txs;
  `GET /api/v1/next-block` estimates the next slot's testing build on the
  current head with the live settings (inclusion list view, tx source; the
  EL mempool only while the mirror runs — the public endpoint never starts
  it): public aggregates incl. `selection.origins`, txs and pool hashes with
  a login; one shared estimate per head, recomputed at most every 2 s or on
  a pool change (the pre-v1 alias is `/api/buildoor/txpool/preview`);
  `DELETE /api/v1/txpool` / `DELETE /api/v1/txpool/{hash}` drop
  (auth + audit); `GET /api/v1/txpool/el-mempool?offset=&limit=&sender=&sort=`
  the EL mempool mirror's stats + a page of it (public: the EL's public txs;
  reading never starts the mirror); `GET /api/v1/txpool/fees` pending
  gas by max fee in log buckets (8/decade) + next base fee + gas limit, the
  pool's private txs as their own series only with a login
- `POST /rpc` - Transaction pool JSON-RPC ingress (NOT under `/api`; see 0d)
- `/debug/pprof/*` - Go profiling, login only: a bearer header (tools) or
  the `buildoor_debug_token` cookie (browser); a browser without it gets the
  WebUI's gate page (`pages/DebugLoginPage.tsx`), which sets the cookie from
  the session (path-scoped, SameSite=Strict, ≤ 30 min) and reloads. Every
  profile download (not the index page) is audited as `debug.pprof`
- `GET /api/v1/head-votes/{slot}?root=&bucket_ms=` - Per-name head-vote
  arrival heatmap: raw single-attestation arrivals grouped by validator-ranges
  client name into fixed-width time buckets from the slot start (default
  slot_ms/24), plus per-name seen/members and the count of attesters that
  landed on chain without being seen as singles. Zero/absent root resolves the
  slot's primary root; only tracker-retained slots (8) are served (404
  otherwise). Fetched by the Head Vote Participation popover's heatmap
- `PATCH /api/v1/config` - Generic path-based global settings update keyed by
  canonical registry keys (`{"epbs.bid_subsidy": 1000, "schedule.mode": "all"}`);
  atomic, unknown keys rejected (auth + audit)

**Real-time events via SSE** (`/api/v1/events`):
- **Connect-time replay burst**: every slot-scoped event is kept in a server-side
  replay cache for the last 5 slots (hard cap 2000 entries) and prefilled into a
  new client's channel at registration, so the UI restores the slot graph and
  event log instead of starting empty. Registration and broadcasting serialize
  on one mutex — replay + live events form a single ordered, gapless stream.
  Every broadcast event carries a monotonic `seq` (seeded from wall-clock micros
  so it survives restarts); the frontend keeps a high-watermark and skips
  replayed events at/below it on reconnect (gap events above it still apply).
  Not cached: `config`/`stats`/`builder_info`/`service_status`/`slot_state`
  (per-client initial-state snapshots, sent without `seq`) and
  `action_plan_updated`/`slot_result_updated` (REST is the source of truth;
  views refetch via `connectionGeneration`). `lifecycle` events are tagged with
  the current slot so they replay too
- **Compression**: the whole router (REST and this stream alike) is gzipped
  by `klauspost/compress/gzhttp` above 1 KB; it flushes with the SSE writer,
  so events still arrive live
- **Cost per viewer** (the UI is public): every event is JSON-encoded once at
  fan-out, not per client; head-vote updates are coalesced to one per curve per
  250 ms (threshold crossings immediate) with percentages rounded to 2
  decimals; open streams are capped (1000 total, 16 per client IP behind
  `txpool.rpc_proxy_hops`, 429 beyond). Content-hashed bundles are served
  immutable
- `config` - the effective config plus `setting_sources` (`{key: {source:
  cli|ui, updated_at}}` for every non-default setting); sent at connect and
  on every settings change (a settings service subscriber, so the timing
  rescale at a slot duration change reaches the viewers too)
- `chain_info` - genesis_time, seconds_per_slot (the GENESIS slot duration in
  ms), slot_segments (`[{epoch, slot, offset_ms, duration_ms}]`, the slot
  timeline), slots_per_epoch, network (CONFIG_NAME), gloas_fork_epoch
  (initial state)
- `slot_outlook` - the next slot before its build: proposer index/client,
  Builder API registration, gossiped proposer preferences, the effective
  (redacted) action plan and whether it is frozen. Sent at connect, every slot
  start and after plan/rule changes
- `bid_won` - Emitted when one of our blocks is seen included at the head (fired from
  the inclusion tracker's `PayloadIncludedEvent.WonBlock`, alongside `bid_included`)
  - Data: slot, block_hash, num_transactions, num_blobs, value_eth, value_wei, timestamp
  - Auto-refreshes first page of Bids Won table
  - Logged in event stream for debugging
- `action_plan_updated` - Fired on committed plan mutations; data is the
  authoritative `{slots: [...], plans: [...]}` change set (null plan = deleted)
- `action_plan_rules_updated` - Fired on committed recurring-rule mutations; data
  is the authoritative `{rules: [...]}` set. The plan grid refetches its range
  because rules change the effective plan of every uncovered slot
- `build_target` / `engine_build` / `testing_build` / `payload_selected` -
  the build events (slot-scoped, replayed): a target's lifecycle, its engine
  build and each testing run with its OWN payload (properties under the block
  hash, the UI's `payloads[hash]`), and the pick changes naming the payload
  the consumers get from then on by hash
- `local_build_status` - The local build status (the `GET
  /api/v1/local-build/status` body: probed EL availability, effective
  settings, pool state, plan check counters), sent at connect and whenever
  its encoding changed (checked once per second, forced after a probe). The
  UI reads it from the stream only, never polls the REST endpoint
- `txpool_stats` - Pool aggregates + counters, sent at connect and whenever the
  pool's version changed (checked once per second)
- `slot_result_updated` - Fired when a slot's result record changes (coalesced to
  ~1/s per slot); data is only `{slot}` — an invalidation, the viewers showing
  the slot refetch it from the REST range endpoint (the record is several KB)
- Neither the stream nor logged-out slot results carry a custom block's
  transaction hashes (`LocalBuildInfo.Public`): pool transactions stay private
  until (and unless) their block is published

#### WebUI Components Pattern

When adding new views:
1. Create hooks in `src/hooks/` for data fetching (see `useBidsWon.ts`)
2. Create components in `src/components/` (see `BidsWonView.tsx`, `BidsWonTable.tsx`)
3. Add TypeScript types to `src/types.ts`
4. Update `App.tsx` for navigation (use conditional rendering, not React Router)
5. If adding backend endpoints, update `pkg/webui/handlers/api/api.go`
6. If adding SSE events, update `pkg/webui/handlers/api/events.go`

### Testing Patterns

- Unit tests are co-located with source files (`*_test.go`)
- Use `testify` for assertions
- Mock RPC clients using interfaces
- Test data in `testdata/` directories

### Logging

All packages use `logrus.Logger` passed as dependency injection:
- Structured logging with `.WithField()` and `.WithFields()`
- Log levels: debug, info, warn, error
- Configure via `--log-level` flag

### Genesis Configuration

For Builder API mode, genesis parameters are automatically fetched from the beacon node:
- `GenesisForkVersion`: Retrieved from the beacon node's genesis endpoint
- `GenesisValidatorsRoot`: Retrieved from the beacon node's genesis endpoint
- These configure the Builder Domain for signature verification

### Bids Won Feature

The Bids Won feature tracks our blocks that were actually included on chain — from
both the Builder API and p2p ePBS flows — providing visibility into block production
outcomes. **Inclusion-time semantics**: entries appear when the block is seen at the
head (via the inclusion tracker's head-event loop), not when it is delivered to a
proposer.

**Backend Architecture:**
- **Detection** (`pkg/payload_bidder/inclusion_tracker.go`): the shared
  `InclusionTracker` detects inclusion at the head (all forks + both flows),
  builds the `WonBlock` summary (source derived from the payload's bid records:
  any Builder-API bid → `builder_api`, otherwise `epbs`) and fires
  `PayloadIncludedEvent` carrying it — no storage here
- **Storage** (`pkg/slot_results/`): the slot results tracker records the
  inclusion on the slot's `SlotResult` and serves the Bids Won wire shape as a
  filtered included-slot view (`GetWonBlocks`, slot-descending). History length
  follows `slot-result-retention-epochs` (default 100 epochs) instead of the old
  1000-entry cap; the legacy `won_blocks` kv namespace is migrated once on start
- `WonBlock` wire fields: source, slot, block_hash, num_transactions,
  num_private_txs (supplied by buildoor itself — own mempool or plan list — vs
  the EL's public mempool; `LocalBuildInfo.PrivateTxs`, absent on older
  records), num_blobs, value_wei, value_eth, timestamp

- **REST API** (`pkg/webui/handlers/api/api.go`):
  - Endpoint: `GET /api/v1/bids-won?offset=0&limit=20`
  - Reads `resultTracker.GetWonBlocks(offset, limit)` (slot-descending);
    offset-based pagination
  - Returns: `BidsWonResponse` with entries array, total count, offset, limit
  - Gracefully handles nil resultTracker (returns empty array)

**Frontend Architecture:**
- **Navigation**: `viewStore` path routing, conditional rendering in `App.tsx`
  (no React Router); the stream state lives in the app-level provider, so it
  survives view switches

- **Data Flow**:
  1. `useBidsWon` hook fetches paginated data on mount and when offset/limit changes
  2. `BidsWonView` component subscribes to SSE `/api/v1/events`
  3. On `bid_won` event, auto-refreshes if on first page (offset === 0)
  4. Other pages require manual navigation/refresh to avoid pagination disruption

- **Components**:
  - `BidsWonTable.tsx`: Responsive table with loading overlay, empty state
  - `Pagination.tsx`: Bootstrap pagination with smart page number display (shows 5 pages max with ellipsis)
  - Click-to-copy block hashes (truncated display: `0x1234...5678`)
  - Relative time formatting: "Just now", "5m ago", "3h ago", or full timestamp

**Key Design Decisions:**
- **Single owner**: win detection is the inclusion tracker's job; storage is the
  slot results tracker's job; the Builder API server does neither (a delivery-time
  record would miss p2p wins and count deliveries that never made it on chain)
- **Memory Management**: epoch-window retention (`slot-result-retention-epochs`)
  prunes memory and the state-db together
- **Real-time Updates**: Only first page refreshes automatically to avoid offset confusion
- **Value Precision**: Store both wei and ETH strings (18-decimal display precision)
- **ePBS Compatibility**: Uses "Bids Won" terminology (not "Payloads Delivered")

**Testing the Feature:**
1. Enable Builder API: `--builder-api-enabled --api-port 18550`
2. Submit a blinded block via `POST /eth/v2/builder/blinded_blocks` and wait for the
   block to appear at the head (or win a slot via p2p bidding)
3. Check backend logs for "Our payload was included in a beacon block!"
4. Open WebUI, click "Bids Won" tab
5. Verify table shows: slot, block hash, transaction count, blob count, value
6. Win another block while on first page → should auto-refresh

### Per-Slot Action Plan Feature

The action plan lets operators script different behavior per slot for Gloas fork
testing: force or suppress bidding/builder-api serving on individual slots (even
against the global enable flags), override bid amounts/windows, delay or inflate
builder-api responses, withhold or delay reveals — and inspect what actually
happened afterwards, down to the exact SSZ objects.

**Semantics** (see `pkg/action_plan/`):
- Global config is the baseline; a slot plan fully overrides it for that slot.
  Categories: `bid`, `builder_api`, `reveal` — absent = inherit, `disabled` =
  suppress, `custom` = force-active with optional overrides
- Plans FREEZE when execution for their slot can begin (~1 slot ahead); frozen and
  past slots reject edits with 409. Operators should plan ≥2 slots ahead
- At-least-once semantics across restarts: an in-flight future slot may be
  re-frozen after a restart
- **Recurring rules** apply a plan template to the same slot index of every epoch
  (`slots_in_epoch`, optional `from_epoch`/`to_epoch`) — the tool for scenarios
  that must hold for a whole devnet run rather than a bounded slot list. An
  explicit plan for a slot replaces the rule wholesale (never merged); a plan
  with `ignore_rules` opts a single slot out. Rules resolve at freeze time, so
  editing them never rewrites a frozen slot

**Outcome inspection** (see `pkg/slot_results/`): every active slot gets an
attempt-level `SlotResult` (REST range queries + `slot_result_updated` SSE) and
raw SSZ artifacts (built payloads by block hash, every signed bid, the signed envelope — stored
at construction, so withheld/failed reveals stay inspectable) served with
SSZ/JSON content negotiation. Blobs bundles are not captured (documented future
artifact kind).

**WebUI**: the Action Plan tab renders an epoch × slot timetable (rows = epochs,
columns = slots) with plan chips + result status per cell, a side drawer that
edits the selected future slots / shows a past slot's full outcome with
artifact downloads, multi-slot/range selection (shift, Ctrl/Cmd) and bulk editing, and a Recurring Rules panel with scenario presets.
Rule-derived cells draw dashed chips; opening one pre-fills the rule's values and
saving detaches that slot into an explicit plan.

**Testing the feature:**
1. Plan a future slot: `curl -X POST .../api/v1/action-plan -d
   '{"updates":[{"slots":[N],"set":{"bid.bid_value_gwei":5000}}]}'`
2. Watch the slot execute (grid cell updates live; `slot_results` records the
   bid attempts with the frozen applied plan)
3. Fetch the exact gossiped bid: `curl -H 'Accept: application/octet-stream'
   .../api/v1/slot-results/N/bids/0 > bid.ssz`
4. Test a withheld reveal: `{"set":{"reveal.mode":"disabled"}}` on a future slot →
   the reveal attempt records `suppressed`/`plan_disabled`, the envelope artifact
   still exists
5. Run it for every epoch instead (the "slot 31 missing" devnet scenario):
   `curl -X POST .../api/v1/action-plan/rules -d '{"rules":[{
   "id":"slot31-withhold","enabled":true,"slots_in_epoch":[31],
   "bid":{"mode":"custom","bid_subsidy":200000000},
   "reveal":{"mode":"disabled"}}]}'` → every slot 31 is bid for and, once won,
   never revealed: the block exists, its execution payload does not. Use
   `bid_subsidy` (added on top of the block value) rather than the absolute
   `bid_value_gwei` to outbid competitors: the absolute value replaces the block
   value entirely and silently loses whenever it is set below what the block is
   worth on that network. A rule that loses the auction withholds nothing, and
   the only symptom is the grid's "bid, not included" dot — size the subsidy
   against the other builders' bids, not against a round number

## Common Issues

1. **"builder-privkey is required"**: Must provide BLS private key (64 hex chars without 0x prefix)
2. **"failed to connect to EL engine API"**: Check JWT secret file path and Engine API URL
3. **Payload building fails**: Ensure `--payload-build-time` is sufficient (default 500ms)
4. **Bids rejected**: Check ePBS timing flags align with network's slot timing
5. **Builder API signature failures**: Genesis parameters are automatically fetched from beacon node; verify beacon node is accessible
6. **Plan edits fail with 409**: The slot is in the past or its plan is already
   frozen (execution began ~1 slot ahead). Plan at least 2 slots into the future
7. **Artifact endpoints return 404 without `--state-db`**: only the newest 64
   slots are kept in the in-memory artifact buffer; set `--state-db` for durable
   artifact history
