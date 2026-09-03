<img align="left" src="./.github/resources/buildoor.png" width="90">
<h1>Buildoor: Ethereum PBS Testing Tool</h1>

> **Work in Progress** - This project is under active development. APIs, configuration flags, and behavior may change without notice.

> **Devnet & Testnet Use Only** - Buildoor is a testing tool designed exclusively for Ethereum devnets and testnets. It is **not** intended to be profitable or used on mainnet. Its sole purpose is to exercise and validate the block builder flow during protocol development and testing.

Buildoor is an Ethereum block builder that supports two modes of operation: **ePBS (enshrined Proposer-Builder Separation)** for the upcoming Gloas fork, and the **traditional Builder API** for pre-ePBS forks (Fulu and earlier). It connects to consensus layer (CL) and execution layer (EL) clients to build blocks, submit bids, and manage builder lifecycle operations.

## Builder Modes

### ePBS (Enshrined Proposer-Builder Separation)

The ePBS mode is designed for the Gloas fork, where proposer-builder separation is enshrined directly into the Ethereum protocol. In this mode, Buildoor:

- Builds execution payloads via the Engine API
- Submits bids to the beacon node on a configurable time schedule relative to slot boundaries
- Reveals payloads at a configured time after the block is proposed
- Tracks bid competition and payload inclusion on-chain
- Requires the builder to be registered on the beacon chain with a deposit (managed via `--lifecycle`)

ePBS is automatically available when the connected beacon node has the Gloas fork epoch configured. Use `--epbs-enabled` to activate bidding/revealing at startup.

### Builder API

The Builder API mode implements the traditional [MEV-Boost Builder API](https://github.com/ethereum/builder-specs) for pre-ePBS forks. In this mode, Buildoor:

- Runs a Builder API HTTP server that validators/relays can connect to
- Accepts validator registrations with fee recipient preferences
- Responds to `getHeader` requests with signed bid headers
- Publishes full blocks when a proposer submits a blinded block via `submitBlindedBlock`
- Supports a configurable block value subsidy to make bids more attractive for testing

Enable with `--builder-api-enabled --builder-api-port <port>`.

## Building

```bash
# Build the binary (includes frontend)
make build

# Build Docker image
make docker

# Run tests
make test
```

Requires Go 1.25+ and Node.js 20+ (for the frontend).

## Usage

### Required Flags

Every run requires these four flags:

| Flag | Description |
|------|-------------|
| `--builder-privkey` | Builder BLS private key (32 bytes hex) |
| `--cl-client` | Consensus layer beacon node URL |
| `--el-engine-api` | Execution layer Engine API URL |
| `--el-jwt-secret` | Path to JWT secret file for Engine API authentication |

### Basic Example

```bash
buildoor run \
  --builder-privkey <BLS_PRIVATE_KEY> \
  --cl-client http://localhost:5052 \
  --el-engine-api http://localhost:8551 \
  --el-jwt-secret /path/to/jwt.hex
```

### ePBS Example

```bash
buildoor run \
  --builder-privkey <BLS_PRIVATE_KEY> \
  --cl-client http://localhost:5052 \
  --el-engine-api http://localhost:8551 \
  --el-jwt-secret /path/to/jwt.hex \
  --epbs-enabled \
  --lifecycle \
  --el-rpc http://localhost:8545 \
  --wallet-privkey <ECDSA_PRIVATE_KEY>
```

### Builder API Example

```bash
buildoor run \
  --builder-privkey <BLS_PRIVATE_KEY> \
  --cl-client http://localhost:5052 \
  --el-engine-api http://localhost:8551 \
  --el-jwt-secret /path/to/jwt.hex \
  --builder-api-enabled \
  --builder-api-port 18550
```

## Configuration Reference

Configuration can be provided via CLI flags, a YAML config file (`--config path/to/config.yaml`), or an environment variables.

### Core Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--builder-privkey` | *(required)* | Builder BLS private key (32 bytes hex) |
| `--cl-client` | *(required)* | Consensus layer beacon node URL |
| `--el-engine-api` | *(required)* | Execution layer Engine API URL |
| `--el-jwt-secret` | *(required)* | Path to JWT secret file |
| `--el-rpc` | | Execution layer JSON-RPC URL (required for lifecycle) |
| `--wallet-privkey` | | Wallet ECDSA private key (required for lifecycle) |
| `--log-level` | `info` | Log level: `debug`, `info`, `warn`, `error` |
| `--config` | | Path to YAML config file |

### Builder API Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--builder-api-enabled` | `false` | Enable the Builder API at startup |
| `--builder-api-port` | `0` | Builder API HTTP port (0 = disabled) |
| `--builder-api-subsidy` | `100000` | Block value subsidy added to bids (Gwei) |

### ePBS Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--epbs-enabled` | `false` | Enable ePBS bidding/revealing at startup |
| `--build-start-time` | `-4000` | Payload build start time in ms relative to slot start |
| `--epbs-bid-start` | `-1000` | First bid time in ms relative to slot start |
| `--epbs-bid-end` | `1000` | Last bid time in ms relative to slot start |
| `--epbs-reveal-time` | `6000` | Payload reveal time in ms relative to slot start |
| `--epbs-bid-min` | `1000000` | Minimum bid amount (Gwei) |
| `--epbs-bid-increase` | `100000` | Bid increase per subsequent bid (Gwei) |
| `--epbs-bid-interval` | `250` | Interval between bids in ms (0 = single bid) |

### Schedule Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--schedule-mode` | `all` | Schedule mode: `all`, `every_nth`, `next_n` |
| `--schedule-every-nth` | `1` | Build every Nth slot (for `every_nth` mode) |
| `--schedule-next-n` | `0` | Build next N slots then stop (for `next_n` mode) |
| `--schedule-start-slot` | `0` | Start building at this slot |

### Lifecycle Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--lifecycle` | `false` | Enable builder lifecycle management (deposits, exits, top-ups) |
| `--deposit-amount` | `10000000000` | Builder deposit amount (Gwei, default 10 ETH) |
| `--topup-threshold` | `1000000000` | Balance threshold for auto top-up (Gwei, default 1 ETH) |
| `--topup-amount` | `5000000000` | Top-up amount (Gwei, default 5 ETH) |

### Other Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--api-port` | `0` | WebUI/API HTTP port (0 = disabled) |
| `--payload-build-time` | `2000` | Time given to the EL to build the payload after fcu (ms) |
| `--validate-withdrawals` | `false` | Validate expected vs actual withdrawals |

### Testing Build Flags

The testing build source replaces the EL txpool with a private transaction
queue and builds blocks through geth's `testing_buildBlockV1`, so a block holds
exactly the transactions you fed it, in the order the packer chose, or the
build fails. See [Testing build source](#testing-build-source).

| Flag | Default | Description |
|------|---------|-------------|
| `--build-source` | `pool` | `pool` (forkchoiceUpdated + getPayload) or `testing` (tx intake + testing_buildBlockV1; needs `--el-rpc` on a geth HTTP RPC with `testing` in `--http.api`) |
| `--testing-fill-gas-pct` | `100` | Share of the block gas limit to pack, 1..100 |
| `--testing-max-txs` | `0` | Max transactions per block (0 = unlimited) |
| `--testing-max-blobs` | `0` | Max blobs per block (0 = the fork's blob limit) |
| `--testing-policy` | `fifo` | Packing order: `fifo`, `fee`, `round_robin`, `as_given` |
| `--testing-base-fee-ceiling-gwei` | `0` | Above this next base fee the fill drops to the 1559 target (0 = off) |
| `--testing-build-deadline` | `0` | Latest build completion in ms relative to slot start (0 = ePBS bid start minus 300 ms) |
| `--testing-on-failure` | `skip` | `skip` the slot or fall back to the `pool` build |
| `--testing-queue-max-txs` | `100000` | Intake queue capacity; submissions beyond it are rejected |
| `--testing-queue-max-age-slots` | `64` | Queued transactions older than this are evicted |
| `--testing-max-attempts` | `3` | Build attempts per slot after attributed EL failures |
| `--testing-max-strikes` | `3` | Attributed build failures before a queued transaction is evicted |

All of them are live settings (`POST /api/config/testing`, or the generic
`POST /api/config/settings` with `build.source` / `testing.*` keys).

## Testing build source

`--build-source testing` turns buildoor into a block load and edge-case tool
for devnets and shadowforks: you decide what a block holds, buildoor gets it
proposed, and it checks the chain honored the plan.

**Transaction intake.** Point any transaction source (spamoor, tx-fuzz, your
own scripts) at `POST http://<buildoor>:<api-port>/rpc` instead of the EL.
It is a JSON-RPC 2.0 endpoint: `eth_sendRawTransaction` lands in buildoor's
private queue, `eth_getTransactionCount(addr, "pending")` answers from the
queue on top of the EL's latest nonce, and every other method is forwarded to
`--el-rpc` verbatim (batches included). Transactions never touch the EL's
public txpool, so nobody else can include them.

**Packing.** At payload_attributes time buildoor puts the EL on the parent,
reads every queued sender's nonce and balance at that parent, and packs the
queue under the block's gas limit, blob cap, byte cap, next base fee and
sender budgets. Nonces below the parent state nonce are evicted, gaps stop a
sender's chain, and the policy orders the rest. `as_given` (or a per-slot
`build.txs` list in the action plan) builds exactly that list in that order;
any deviation is an error, never a trim.

**Build.** The list goes to `testing_buildBlockV1`. geth refuses the whole
block if one transaction is invalid. Under a fill policy buildoor attributes
the error to the sender or index, drops those, and retries within
`--testing-max-attempts`. Under `as_given` it does not: an explicit list is a
contract, so a refusal fails the build rather than quietly building a smaller
block that would then verify as a "match" against the reduced plan.
The built payload is checked against the plan before it is bid or served.
Only geth implements the method today, and only on the HTTP port with
`testing` in `--http.api`. Speculative build candidates are skipped in this
mode: geth builds on its current head only.

**Verification.** Every included testing-built payload is fetched from the
EL and compared with its plan: same transactions, same order, same count.
The verdict lands on the slot result (`tx_plan.status`: `match`,
`mismatch`, `block_not_found`, `missed`, `orphaned`), in the
`buildoor_testing_plan_checks_total` metric, and as an error log line
starting with `TX PLAN CHECK FAILED`.

**Per-slot control** through the action plan's `build` category:
`source`, `fill: {gas_pct, max_txs, max_blobs, policy}` and `txs` (explicit
ordered hashes, must be queued). Recurring rules script patterns such as a
full block every fourth slot.

**Everything must go through the intake.** While the testing source is
active the builder's blocks are the only way anything gets included: a
transaction sent to the EL's normal RPC sits in the public txpool forever,
because buildoor builds solely from its queue. That covers the obvious case
(your load generator) and the easy one to miss (its wallet funding and
refills, contract deployments, and any other tool pointed at the same
network). Point the whole transaction source at `/rpc`, not just its sending
path. buildoor's own lifecycle transactions are already teed into the queue
for this reason.

**Watch out for base fee.** Every full block raises the base fee by 12.5
percent, and buildoor's own blocks are the only ones carrying its queue. Give
your transaction source a high max fee or set
`--testing-base-fee-ceiling-gwei` so a long run stays sustainable.
buildoor's own lifecycle transactions go through the intake too, so its
deposits and top-ups land in the blocks it builds.

```bash
# geth: --http.api admin,engine,net,eth,web3,debug,txpool,testing
buildoor run ... --el-rpc http://geth:8545 --build-source testing --api-port 8080
spamoor eoatx --rpchost http://buildoor:8080/rpc --privkey <key> -t 300 --basefee 100
curl http://buildoor:8080/api/buildoor/tx-queue          # queue + plan check tally
curl "http://buildoor:8080/api/buildoor/slot-results?min_slot=100&max_slot=110" | jq '.[].tx_plan'
```

## WebUI

Buildoor includes a web dashboard for monitoring builder activity in real time. Enable it with `--api-port <port>` and open `http://localhost:<port>` in your browser.

The dashboard provides:
- Real-time slot timeline and bid tracking via Server-Sent Events (SSE)
- Builder statistics and configuration management
- Bids Won tracking (Builder API mode)
- Validator registration overview

## Local Development

```bash
# Start a local devnet
make devnet

# Run buildoor against the devnet
make devnet-run-docker

# Clean up the devnet
make devnet-clean
```

The devnet launches canonical buildoor instances via the ethereum-package
(`buildoor_params.instances` in `.hack/devnet/custom-kurtosis.devnet.config.yaml`).
`make devnet-run` / `make devnet-run-docker` stop the first canonical instance and
run your local build in its place — same builder key, wallet and service name — so
it receives the CL's builder API calls. Use `BUILDOOR_INDEX=<n>` (or
`BUILDOOR_SERVICE=<name>`) to replace a different instance.

For frontend development:

```bash
cd pkg/webui
npm install
npm run dev    # watch mode
```

## Docker

```bash
# Build
docker build -t buildoor .

# Run
docker run --rm buildoor run --help
```

## License

See [LICENSE](LICENSE) for details.
