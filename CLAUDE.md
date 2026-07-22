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
  --epbs-reveal-time 4000

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
  --builder-api-port 18550

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
     before it feeds both the bid commitment and the reveal (Payload.BlockHash
     re-synced from the result); bid/envelope rewrite the MESSAGE just before signing
     and are then RE-SIGNED (target-slot fork) — so results are validly signed but
     customized, and a bid commitment can deliberately diverge from the revealed
     payload. Expressions are jq-Validated at plan-update time (400 on bad jq); a
     runtime transform failure fails that construction (loud, recorded). Round-trip is
     MarshalJSON→gojq→UnmarshalJSON into a fresh object with Version preset (via
     `jqtransform.ApplyTyped`). Live-tested from the UI through
     `POST /api/buildoor/action-plan/test-transform` (target+expression+optional
     sample_slot → runs the exact production gojq against a captured artifact, else
     the latest buffered one, else an illustrative template; bid/envelope reduced to
     `.message`).
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
     `slot-result-retention-epochs`, future plans never.

1. **Builder Service** (`pkg/payload_builder/`)
   - Main orchestrator for payload building
   - Subscribes to beacon node's `payload_attributes` events
   - Freezes the slot's action plan and acts on `frozen.Build`: decision, skip
     reason, build start time; reports `OnSlotBuilt` back for next_n accounting
     (plan-forced builds don't consume the budget)
   - Calls Engine API to construct execution payloads (forkchoiceUpdated → getPayload)
   - Emits `PayloadReadyEvent` to subscribers; plan-involved skips fire
     `BuildSkippedEvent` (deduped per slot) for the slot results tracker

2. **Payload Bidder** (`pkg/payload_bidder/`) — shared Gloas+ bid/reveal domain
   - `Signer`, `BuildSignedBid`, `BuildSignedEnvelope`: bid/envelope construction + signing
   - `RevealService`: the ONLY envelope publisher. Own main loop (channel + timer, no
     polling); both flows request reveals via `RequestReveal`; dedupes per slot (exactly
     one publish per won slot); timing/suppression come from the slot's frozen plan
     (`frozen.Reveal`: plan-disabled slots fire a terminal skipped result with reason
     `plan_disabled`; custom reveal times may bypass the in-slot deadline for
     late-reveal testing, clamped to slot end + 1 slot); envelope construction is
     split from publish (`buildEnvelope`) so every per-attempt `RevealResult` carries
     the built envelope — failed publishes stay inspectable; envelope signing uses
     the TARGET slot's fork; retries ×3
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
   - Caches last 2 epochs of state
   - Detects fork transitions (Electra → Gloas)
   - Loads builder registrations from beacon state (post-Gloas)
   - Provides slot↔timestamp conversions
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
     (retention 8 slots, in-memory only). **Subnet coverage detection**: every imported
     block's attestations (fetched per head event, aggregate-format walk) are
     the ground truth compared against the singles bitmap; a rolling 16-block
     window seeing <80% of on-chain attesters (min 8 blocks / 16 attesters,
     recover at ≥90%) sets the `Low` flag on the `vote_coverage` SSE event
     (initial-state + on change) — the UI then shows a warning badge (hover
     callout with the subscribe-all-subnets flags) in the Slot Timeline
     header and hides the head-vote graph as unreliable

4. **Lifecycle Manager** (`pkg/lifecycle/`)
   - Builder registration on beacon chain
   - Balance monitoring and auto top-ups
   - Deposit and exit operations
   - Optional component (only active with `--lifecycle` flag)

4b. **Slot Results Tracker** (`pkg/slot_results/`) — generic per-slot outcome history
   - One attempt-aware `SlotResult` per slot where ePBS or the Builder API was active:
     build lifecycle (incl. `waiting_attributes`/`no_attributes` baselines from a slot
     clock so "planned but nothing happened" is visible), bid attempts (both
     transports, with statuses/competitor context/artifact refs), block submissions,
     reveal attempts, inclusion — plus the frozen `applied_plan` snapshotted at record
     creation. Copy-on-write records; attempts cap at 256/kind with a dropped counter;
     SSE updates coalesce per slot
   - Consumes the services' BLOCKING subscriptions (loss-free history) and implements
     the `builderapi.SlotResultRecorder` interface for request-scoped recording
   - `ArtifactStore`: raw SSZ artifacts per slot — built payload, every signed bid
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
     module is globally disabled. The `enabled` flag keeps gating only the
     non-slot-scoped endpoints (registrations, preferences, block submission).
     Frozen per-slot values drive subsidy, absolute total bid value (uint256 wei
     math) and a context-cancellable response delay. Bid requests beyond
     `currentSlot+1` are rejected with 400 before freezing
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
   - React/TypeScript dashboard
   - Real-time event stream via Server-Sent Events (SSE)
   - Visual slot timeline, bid tracking, validator registrations
   - Configuration updates via HTTP API (incl. the generic path-based
     `POST /api/config/settings`)
   - **Action Plan View**: epoch × slot timetable (rows = epochs, columns =
     slots) rendering plan chips + result status per cell; click-to-edit modal
     for future slots, full outcome detail + SSZ/JSON artifact downloads for
     past slots; multi-slot/range bulk editing; live via the
     `action_plan_updated`/`slot_result_updated` SSE events
   - **Bids Won View**: Paginated table of our blocks included on chain
     - Tab navigation: Dashboard / Bids Won / Action Plan
     - Real-time updates when new blocks are included (bid_won SSE from the
       inclusion tracker's `PayloadIncludedEvent`)
     - Click-to-copy block hashes, relative timestamps
     - Shows: slot, block hash, # transactions, # blobs, value in ETH

### Event Flow

```
Beacon Node ─payload_attributes─▶ payload_builder ─PayloadReady─▶ p2p_bidder (bids via gossip)
Beacon Node ─head events────────▶ payload_bidder.InclusionTracker ─▶ RevealService ─▶ Beacon Node
Proposer ───Builder API─────────▶ builderapi (legacy: unblind+publish │ epbs: block broadcast
                                              + RevealService.RequestReveal)
```

### Data Flow (ePBS Mode)

1. Beacon node emits `payload_attributes` event
2. Builder validates slot against schedule and builds at `BuildStartTime`
   (Engine API: forkchoiceUpdated → getPayload), emits `PayloadReadyEvent`
3. p2p_bidder scheduler ticks every 10ms and submits bids inside the slot's FROZEN
   bid window (per-slot plan > global config; gated on builder registration and, per
   slot, on the frozen enable resolution)
4. Head event → InclusionTracker matches the block against our payload cache; on a win
   it records the pending payment, requests the reveal, and fires the inclusion event
   (the slot results tracker stores the outcome)
5. RevealService publishes the envelope at the slot's frozen reveal time, deduped per
   slot — a Builder-API-won block's reveal was already requested by the epbs dialect
   handler at block submission, so the p2p-side request is a no-op. Plan-suppressed
   slots skip with reason `plan_disabled`
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
- YAML config file (`--config` flag or `./buildoor.yaml`)
- Environment variables (auto-loaded by viper)

Key config sections:
- **Builder keys**: `--builder-privkey` (BLS), `--wallet-privkey` (ECDSA)
- **Clients**: `--cl-client`, `--el-engine-api`, `--el-rpc`
- **Schedule**: `--schedule-mode` (all/every_nth/next_n), `--schedule-every-nth`, `--schedule-next-n`
- **ePBS timing**: `--build-start-time`, `--epbs-bid-start`, `--epbs-bid-end`, `--epbs-reveal-time`
- **Bidding**: `--epbs-bid-min`, `--epbs-bid-increase`, `--epbs-bid-interval`,
  `--epbs-bid-value-override` (absolute p2p bid base, 0 = off),
  `--epbs-vote-threshold` (head-vote participation threshold in percent,
  default 60, 0 = off),
  `--builder-api-value-override` (absolute served total value, 0 = off)
- **Slot history**: `--slot-result-retention-epochs` (default 100),
  `--slot-artifact-retention-epochs` (default 100; raw payloads dominate disk),
  `--slot-artifact-capture-enabled` (default true)
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

The mutable-setting registry lives in `pkg/settings/fields.go` (keys in `keys.go`);
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
  `action_plan`), `slot_results` (per-slot outcome summaries, JSON; codec in
  `slot_results`; the legacy `won_blocks` namespace is migrated into it once on
  startup and then deleted)
- `slot_artifacts` — dedicated table (NOT a kv namespace: blobs are too large for
  the in-RAM memstore pattern and pruning needs a SQL range delete on the integer
  slot column) holding raw SSZ artifacts per slot (payload/bid/envelope), fed by
  the tracker's async batching writer. SQLite does not shrink the file on delete
- `audit_log` — every authenticated mutating action (actor from JWT subject)

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
│   │                      # freeze semantics (FrozenPlan = raw plan + resolved
│   │                      # effective settings + complete build decision), atomic
│   │                      # bulk updates w/ path-based partial edits, kv codec
│   ├── slot_results/      # generic per-slot outcome history: attempt-aware
│   │                      # SlotResult tracker (blocking subscriptions, baseline
│   │                      # slot clock, won-block view, won_blocks migration) +
│   │                      # ArtifactStore (raw SSZ payload/bids/envelope, async
│   │                      # batched writer into the slot_artifacts table)
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
- Real-time updates via SSE at `/api/events`
- Custom hook `useEventStream()` manages app state from events
- Backend serves static files via `pkg/webui/static/`
- **Navigation**: Simple conditional rendering without React Router (Dashboard / Bids Won tabs)
- **Styling**: Bootstrap 5 CSS with custom components

To make frontend changes:
1. `cd pkg/webui && npm run dev` (watch mode)
2. Backend serves from `static/` automatically
3. Rebuild binary only if adding new API endpoints

#### WebUI API Endpoints

**Buildoor-specific endpoints:**
- `GET /api/buildoor/validators` - List registered validators
- `GET /api/buildoor/bids-won` - Paginated list of won blocks (read from the slot
  results tracker's included-slot view; Builder API and p2p ePBS wins alike)
  - Query params: `offset` (default: 0), `limit` (default: 20, max: 100)
  - Returns: `{ bids_won: [], total: number, offset: number, limit: number }`
- `GET /api/buildoor/builder-api-status` - Builder API configuration and validator count
- `GET /api/buildoor/action-plan?min_slot=&max_slot=` - Per-slot action plans in the
  inclusive range (max span 320 epochs)
- `POST /api/buildoor/action-plan` - Atomic bulk plan mutation (auth + audit).
  Body `{updates: [...]}`; each update targets `slots` and/or `from_slot..to_slot`,
  with three-state category members (absent/null/object) and fine-grained `set`
  paths (`{"bid.bid_min_amount": 5000}`). Past/frozen slots → 409. Returns the
  authoritative normalized plans
- `GET /api/buildoor/slot-results?min_slot=&max_slot=` - Attempt-level outcome
  history per slot (build, bids, submissions, reveals, inclusion, applied plan)
- `GET /api/buildoor/slot-results/{slot}/payload|envelope|bids|bids/{index}` - Raw
  SSZ artifacts with beacon-API content negotiation: `Accept:
  application/octet-stream` → exact SSZ bytes, otherwise `{"version", "data"}`
  JSON; responses carry `Eth-Consensus-Version` + `Vary: Accept`; the bid listing
  is JSON-only metadata
- `GET /api/buildoor/head-votes/{slot}?root=&bucket_ms=` - Per-name head-vote
  arrival heatmap: raw single-attestation arrivals grouped by validator-ranges
  client name into fixed-width time buckets from the slot start (default
  slot_ms/24), plus per-name seen/members and the count of attesters that
  landed on chain without being seen as singles. Zero/absent root resolves the
  slot's primary root; only tracker-retained slots (8) are served (404
  otherwise). Fetched by the Head Vote Participation popover's heatmap
- `POST /api/config/settings` - Generic path-based global settings update keyed by
  canonical registry keys (`{"epbs.bid_subsidy": 1000, "schedule.mode": "all"}`);
  atomic, unknown keys rejected (auth + audit)

**Real-time events via SSE** (`/api/events`):
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
- `chain_info` - genesis_time, seconds_per_slot, slots_per_epoch (initial state)
- `bid_won` - Emitted when one of our blocks is seen included at the head (fired from
  the inclusion tracker's `PayloadIncludedEvent.WonBlock`, alongside `bid_included`)
  - Data: slot, block_hash, num_transactions, num_blobs, value_eth, value_wei, timestamp
  - Auto-refreshes first page of Bids Won table
  - Logged in event stream for debugging
- `action_plan_updated` - Fired on committed plan mutations; data is the
  authoritative `{slots: [...], plans: [...]}` change set (null plan = deleted)
- `slot_result_updated` - Fired when a slot's result record changes (coalesced to
  ~1/s per slot); data is the full SlotResult. SSE is an invalidation channel —
  the REST range endpoints are the source of truth

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
- `WonBlock` wire fields (unchanged): source, slot, block_hash, num_transactions,
  num_blobs, value_wei, value_eth, timestamp

- **REST API** (`pkg/webui/handlers/api/api.go`):
  - Endpoint: `GET /api/buildoor/bids-won?offset=0&limit=20`
  - Reads `resultTracker.GetWonBlocks(offset, limit)` (slot-descending);
    offset-based pagination
  - Returns: `BidsWonResponse` with entries array, total count, offset, limit
  - Gracefully handles nil resultTracker (returns empty array)

**Frontend Architecture:**
- **Navigation**: Tab-based UI in `App.tsx` (Dashboard / Bids Won)
  - Uses `useState<ViewType>` for view switching
  - Conditional rendering without React Router
  - Preserves Dashboard state when switching views

- **Data Flow**:
  1. `useBidsWon` hook fetches paginated data on mount and when offset/limit changes
  2. `BidsWonView` component subscribes to SSE `/api/events`
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
1. Enable Builder API: `--builder-api-enabled --builder-api-port 18550`
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

**Outcome inspection** (see `pkg/slot_results/`): every active slot gets an
attempt-level `SlotResult` (REST range queries + `slot_result_updated` SSE) and
raw SSZ artifacts (built payload, every signed bid, the signed envelope — stored
at construction, so withheld/failed reveals stay inspectable) served with
SSZ/JSON content negotiation. Blobs bundles are not captured (documented future
artifact kind).

**WebUI**: the Action Plan tab renders an epoch × slot timetable (rows = epochs,
columns = slots) with plan chips + result status per cell, click-to-edit modal
(future slots) / full outcome detail with artifact downloads (past slots), and
multi-slot/range bulk editing.

**Testing the feature:**
1. Plan a future slot: `curl -X POST .../api/buildoor/action-plan -d
   '{"updates":[{"slots":[N],"set":{"bid.bid_value_gwei":5000}}]}'`
2. Watch the slot execute (grid cell updates live; `slot_results` records the
   bid attempts with the frozen applied plan)
3. Fetch the exact gossiped bid: `curl -H 'Accept: application/octet-stream'
   .../api/buildoor/slot-results/N/bids/0 > bid.ssz`
4. Test a withheld reveal: `{"set":{"reveal.mode":"disabled"}}` on a future slot →
   the reveal attempt records `suppressed`/`plan_disabled`, the envelope artifact
   still exists

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
