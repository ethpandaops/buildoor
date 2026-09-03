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

### Local Build & Transaction Pool Flags

Buildoor can build an additional payload per slot through the EL's
`testing_buildBlockV1` (execution-apis `testing` namespace, disabled by default
on every client — enable it with `--http.api ...,testing` on geth/reth/erigon,
`--rpc-http-api ...,TESTING` on besu, `--JsonRpc.EnabledModules ...,Testing` on
nethermind) from a transaction source it controls, and choose whether bids use
that payload or the EL's. Under the kurtosis ethereum-package the module flag
can only be appended through `el_extra_params`: geth, besu and erigon accept the
repeated flag (the appended list wins), nethermind rejects it — expose the
namespace on an additional endpoint instead
(`--JsonRpc.AdditionalRpcUrls=http://0.0.0.0:8547|http|net;eth;web3;txpool;testing`
and point `--el-rpc` at port 8547) — and reth rejects it without an alternative. Both extensions need `--el-rpc` and are off by
default; the enable settings are refused while the EL does not expose the
namespace (probed at startup and every 5 minutes, or on demand from the UI).

| Flag | Default | Description |
|------|---------|-------------|
| `--local-build-enabled` | `false` | Build the additional local payload per slot |
| `--local-build-payload-source` | `el` | Which payload feeds bids/reveals: `el` (local is a shadow), `local`, `local_or_el` |
| `--local-build-tx-source` | `txpool` | Transaction source: `txpool`, `empty`, `el_mempool` (per-slot plans may set an explicit list) |
| `--local-build-el-payload` | `true` | Keep the engine-API build when the payload source is `local` |
| `--local-build-allow-blobs-without-bundle` | `false` | Include blob txs on ELs whose testing path returns no blobs bundle (reth, ethrex) |
| `--local-build-blob-encoding` | `auto` | Blob tx encoding for the EL: `auto`, `network`, `canonical` |
| `--txpool-enabled` | `false` | Owned transaction pool + JSON-RPC ingress at `/rpc` on `--api-port` |
| `--txpool-auth-token` | | Bearer token required on `/rpc` |
| `--txpool-ordering` | `fifo` | Block selection order: `fifo`, `tip`, `random` |
| `--txpool-block-max-txs` | `0` | Max pool txs per local block (0 = unlimited) |
| `--txpool-gas-fill-pct` | `100` | Share of the block gas limit the selection fills |
| `--txpool-max-txs` | `10000` | Pool capacity |
| `--txpool-max-txs-per-sender` | `256` | Per-sender queue cap |
| `--txpool-tx-ttl-slots` | `0` | Drop queued txs older than this many slots (0 = never; expiring queued txs leaves nonce gaps behind) |
| `--txpool-forward-to-el` | `false` | Also submit admitted txs to the EL mempool (A/B shadow mode) |

Point a generator at the pool as its ONLY host:

```bash
spamoor eoatx -h "name(buildoor)http://<buildoor>:8080/rpc" -p <privkey>
```

The ingress answers `eth_sendRawTransaction` (and `eth_chainId`,
`eth_getTransactionCount` pool-aware for `pending`, `txpool_*`) locally and
proxies read-only `eth_*`/`net_*`/`web3_*` calls to `--el-rpc`, so spamoor can
follow the chain through it. Do not add the EL as a second host: spamoor fans
every submission out to all of its hosts, which would put the transactions into
the EL mempool as well. Pool transactions never enter the EL mempool: they land
on chain only inside a local payload that won the slot.

### Other Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--api-port` | `0` | WebUI/API HTTP port (0 = disabled) |
| `--payload-build-time` | `2000` | Time given to the EL to build the payload after fcu (ms) |
| `--validate-withdrawals` | `false` | Validate expected vs actual withdrawals |

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

# Feed the replacement's transaction pool with a one-shot spamoor
make devnet-spam

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
