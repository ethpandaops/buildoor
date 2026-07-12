# Per-slot action plan, slot outcome history & builder testing knobs

Adds a per-slot **action plan** to buildoor so operators can script different
behaviour for individual slots during Gloas/ePBS fork testing, records the
**outcome** of every active slot (down to the exact SSZ objects), and surfaces
both in a new **Action Plan** WebUI tab. On top of that it adds targeted
testing knobs — parent-payload reorg builds and arbitrary **jq transforms** of
the payload/bid/envelope — plus a batch of WebUI robustness fixes.

Buildoor is a Gloas/ePBS development & testing tool, **not** a mainnet builder;
several features here (underbidding, parent reorg, arbitrary object rewriting)
are deliberately adversarial and are rejected by mainnet forkchoice.

Branch: `pk910/testing-capabilities` · base: `main` (`2c374b8`)

---

## What you can do now

- **Script any slot.** Force or suppress bidding (p2p), Builder-API bid serving,
  and reveal on individual slots — even against the global enable flags — and
  override amounts, windows, response delays, reveal timing. Plan far ahead; a
  slot's plan freezes ~1 slot before execution and becomes immutable.
- **Inspect what happened.** Every slot where ePBS or the Builder API was active
  gets an attempt-level result record (build lifecycle, every bid with competitor
  context, block submissions, reveal attempts, inclusion + canonical verdict) with
  the frozen plan snapshotted in, plus the exact SSZ artifacts (built payload,
  every signed bid, the envelope — even when a reveal was withheld) served as SSZ
  or JSON via `Accept` negotiation.
- **See it live.** An epoch × slot timetable with plan chips + a two-dot outcome
  per cell (bid status / canonical payload verdict), click-to-edit modals, bulk /
  range editing, artifact downloads, all updating live over the existing SSE
  connection.
- **Reorg the parent payload.** Per-slot flag to build on the grandparent (n-2)
  execution payload — a deliberate parent-payload reorg attempt.
- **Rewrite the objects.** Per-slot jq expressions that modify the payload, bid
  message, or envelope message (bid/envelope re-signed) — for custom builder
  behaviour the tool isn't aware of — with a live in-modal expression tester.

---

## Architecture

Four new packages and an extended data model, wired as a single per-slot
authority feeding downstream consumers by frozen snapshots.

### `pkg/action_plan` — the per-slot scheduling authority

The `PlanService` is the **single** owner of per-slot scheduling and settings
(the old `SlotManager` is gone; the schedule modes all/every_nth/next_n and
next_n accounting live here). Consumers receive it as a mandatory constructor
dependency and poll it — no module interprets raw plans or schedule config
itself, no nil-guards, no fallback paths.

- **Sparse plans.** Categories `bid` / `builder_api` / `reveal` are three-state:
  absent = inherit the global baseline, `disabled` = suppress, `custom` =
  force-active (even when the module is globally disabled) with optional
  overrides. Two further **modeless** categories: `build` (`reorg_parent_payload`)
  and `transforms` (jq expressions).
- **Freeze semantics.** `Freeze(slot)` resolves an immutable `FrozenPlan` — raw
  plan + effective settings merged from the live global config + the target-slot
  fork + the complete build decision — the first time any decision point touches
  the slot; every later caller gets the identical snapshot, so later config
  changes never rewrite a partially executed slot. Edits to past or frozen slots
  fail with `ErrSlotLocked` (HTTP 409).
- **Atomic bulk mutations.** `ApplyUpdates` is all-or-nothing, supports slot
  lists + inclusive ranges, three-state category patches, and fine-grained `set`
  paths (`"bid.bid_min_amount": 5000`) so consumers never send a full category
  object for a partial edit. Committed changes fire `PlanChangeEvent`.
- Persisted via the `kv_store` `slot_plans` namespace; past plans prune to
  retention, future plans never.

### `pkg/slot_results` — generic per-slot outcome history

- **Attempt-aware `SlotResult`** per active slot: build lifecycle (incl.
  `waiting_attributes`/`no_attributes` baselines from a slot clock, so "planned
  but nothing happened" is visible), bid attempts (both transports), block
  submissions, reveal attempts, inclusion + a reorg-aware canonical payload
  verdict — plus the frozen `applied_plan`. Copy-on-write records, attempt caps
  with a dropped counter, coalesced SSE updates. Consumes the producer services'
  **blocking** subscriptions so history is loss-free.
- **`ArtifactStore`** — raw SSZ artifacts per slot (payload / every signed bid /
  envelope) with a write-through 64-slot memory buffer plus an async batching
  writer into a dedicated `slot_artifacts` SQLite table (blobs are too large for
  the in-RAM memstore pattern; pruning needs a SQL range delete). Hot paths never
  wait on SQLite.
- Serves the existing **Bids Won** view as a filtered included-slot view and
  migrates the legacy `won_blocks` kv namespace once on startup (merge-safe,
  idempotent, crash-safe).

### `pkg/jqtransform` — sandboxed jq

Wraps `itchyny/gojq` (pure Go, no I/O): environment access disabled,
single-output enforced, context-timeout. `ApplyTyped` round-trips a
fork-agnostic object through `MarshalJSON → gojq → UnmarshalJSON` into a fresh
object with `Version` preset.

### `pkg/db` — `slot_artifacts` table

New goose migration (`00003_slot_artifacts.sql`) + repository:
`(slot, kind, idx)` primary key, batched insert, range-delete prune.

---

## Feature detail

### Reorg-aware canonical payload verdicts

The inclusion tracker no longer does a one-shot follow-up check. Each won slot is
tracked for a 16-slot window; on every head event it walks the head's ancestry
(cached by block root) down to the won slot to derive a verdict — `canonical`
(the next canonical block builds on our payload), `missed` (builds on an older
execution block: payload withheld / late / voted empty), or `orphaned` (the won
block itself was reorged out). **Reorgs flip verdicts**; every change fires a
`PayloadStatusEvent` recorded on `inclusion.payload_status` and pushed over SSE.

### Parent-payload reorg build (`build.reorg_parent_payload`)

Builds the slot's payload on the grandparent (n-2) execution payload: the FCU
head hash, the parent block **number**, and the withdrawals come from the parent
slot's cached payload attributes (whose parent is n-2), while every other
property — including the beacon parent root — stays from the current slot. The
effective attributes are built once and stored on the `Payload`, so the built
payload **and** the bid derived from it agree on the reorged parent. Falls back
to a normal build (logged) when the parent slot's attributes are unavailable.

### jq transforms (`transforms.payload` / `.bid` / `.envelope`)

- **payload** rewrites the built execution payload before it feeds both the bid
  commitment and the reveal (`Payload.BlockHash` re-synced from the result).
- **bid** / **envelope** rewrite the **message** just before signing, then
  **re-sign** with the target-slot fork — so results are validly signed but
  customized, and a bid commitment can deliberately diverge from the revealed
  payload.
- Expressions are jq-validated at plan-update time (400 on bad jq); a runtime
  transform failure fails that construction loudly rather than signing the
  untransformed object.
- **Live testing:** `POST /api/buildoor/action-plan/test-transform` runs the
  exact production gojq against a captured artifact for the slot (bid/envelope
  reduced to `.message`), else the latest buffered artifact, else an illustrative
  template — powering a debounced input/output preview in the edit modal.

### SSE connect-time replay

Every slot-scoped event is kept in a 5-slot server-side replay cache and
prefilled into a new client's channel at registration (registration and
broadcasting serialize on one mutex → one ordered, gapless stream). Events carry
a monotonic `seq` (seeded from wall-clock micros to survive restarts); the
frontend keeps a high-watermark and drops replayed duplicates on reconnect. The
UI now restores the slot graph and event log on connect instead of starting
empty.

### Security fix — Builder API plan-freeze DoS guard

`getHeader` / `getExecutionPayloadBid` reject slots beyond `currentSlot+1` (400)
**before** freezing, so a client cannot lock arbitrary future slot plans.

---

## API surface

**REST** (`pkg/webui/handlers/api`):

- `GET /api/buildoor/action-plan?min_slot=&max_slot=` — per-slot plans in range.
- `POST /api/buildoor/action-plan` — atomic bulk mutation (three-state
  categories + `set` paths); past/frozen slots → 409 (auth + audit).
- `POST /api/buildoor/action-plan/test-transform` — evaluate a jq expression
  against a sample object.
- `GET /api/buildoor/slot-results?min_slot=&max_slot=` — attempt-level history.
- `GET /api/buildoor/slot-results/{slot}/payload|envelope|bids|bids/{index}` —
  raw SSZ artifacts with `Accept`-based SSZ/JSON content negotiation.
- `POST /api/config/settings` — generic path-based global settings update.
- `GET /api/buildoor/bids-won` — now served from the slot results tracker.

**SSE** (`/api/events`): `action_plan_updated`, `slot_result_updated`, the
connect-time replay burst + `seq`, plus the existing event set.

**Config:** `--slot-result-retention-epochs` (100), `--slot-artifact-retention-epochs`
(100), `--slot-artifact-capture-enabled` (true), `--epbs-bid-value-override`,
`--builder-api-value-override`.

---

## Notable fixes bundled in

- **`Dispatcher.Unsubscribe` never removed subscriptions** (reversed guard) —
  this leak also blocked loss-free blocking subscriptions; fixed with tests.
- **Bid & envelope signing used the current fork instead of the target slot's
  fork** — wrong at the first Gloas slot; both fixed.
- **Legacy Builder-API subsidy `uint64` overflow** (`subsidy * 1e9`) — moved to
  uint256.
- WebUI: Bids Won infinite refetch loop on the SSE replay burst; Action Plan grid
  not applying live `slot_result_updated` (slot marshaled as a JSON string);
  withheld reveals rendered as successful; replayed slots not shown in the
  timeline; transform test input/output rendered as objects (React #31).

---

## Data & migration notes

- Everything is **in-memory unless `--state-db <path>` is set**. With it:
  settings, `kv_store` (incl. `slot_plans`, `slot_results`), and the
  `slot_artifacts` table persist across restarts; the legacy `won_blocks`
  namespace is migrated into `slot_results` once and deleted.
- One new goose migration (`00003_slot_artifacts.sql`), applied automatically.
- No breaking changes to existing wire shapes (Bids Won unchanged).

---

## Testing

- New unit tests for `action_plan` (freeze truth table, bulk/path updates,
  build & transform categories), `slot_results` (tracker, artifacts, migration),
  `jqtransform` (env-blocking, single-output, cancellation), the inclusion
  tracker's reorg verdicts, the bid/envelope transform round-trip + re-sign, the
  SSE replay cache, and the transform test endpoint.
- Verified: `make build`, full `go test ./...`, `-race` on the touched packages,
  `tsc --noEmit`, and the production webpack build — all green.

**Manual smoke test (devnet):**

1. Plan a slot ≥2 ahead: `curl -X POST .../api/buildoor/action-plan -d
   '{"updates":[{"slots":[N],"set":{"bid.bid_value_gwei":5000}}]}'`.
2. Watch the grid cell update live as the slot executes.
3. Pull the exact gossiped bid: `curl -H 'Accept: application/octet-stream'
   .../api/buildoor/slot-results/N/bids/0 > bid.ssz`.
4. Try a withheld reveal (`{"set":{"reveal.mode":"disabled"}}`), a parent reorg
   (`{"set":{"build.reorg_parent_payload":true}}`), and a jq transform
   (`{"set":{"bid.gas_limit... "}}` via the modal's live tester).
