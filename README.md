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

Enable with `--builder-api-enabled --api-port <port>` (the Builder API is served on the WebUI/API port).

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
  --api-port 18550
```

## Configuration Reference

Configuration can be provided via CLI flags, a YAML config file (`--config path/to/config.yaml`), or environment variables.

- **Config file**: a flat YAML map keyed by the flag names without the leading dashes (`deposit-amount: 32000000000`). Any other key fails the start instead of being ignored.
- **Environment**: `BUILDOOR_` + the flag name in upper case with dashes as underscores (`BUILDOOR_DEPOSIT_AMOUNT=32000000000`).
- Values that a module would not act on (unknown modes, strategies, out-of-range percentages) fail the start as well.

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
| `--builder-api-subsidy` | `100000` | Block value subsidy added to bids (Gwei) |

### ePBS Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--epbs-enabled` | `false` | Enable ePBS bidding/revealing at startup |
| `--build-start-time` | `0` | Payload build start time in ms relative to slot start (0 = auto: -3400 ms @12s, scaled to slot time) |
| `--epbs-bid-start` | `0` | First bid time in ms relative to slot start (0 = auto: -400 ms @12s) |
| `--epbs-bid-end` | `0` | Last bid time in ms relative to slot start (0 = auto: -100 ms @12s) |
| `--reveal-time` | `0` | Payload reveal time gate in ms relative to slot start (0 = auto: 5000 ms @12s) |
| `--epbs-bid-min` | `1000000` | Minimum bid amount (Gwei) |
| `--epbs-bid-increase` | `100000` | Bid increase per subsequent bid (Gwei) |
| `--epbs-bid-interval` | `500` | Interval between bids in ms (0 = single bid) |

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
| `--deposit-amount` | `50000000000` | Amount of every builder key deposit — early onboarding, registration and each top-up (Gwei, default 50 ETH). There is no separate top-up amount |
| `--topup-threshold` | `10000000000` | Effective balance below which a key is topped up by `--deposit-amount` (Gwei, default 10 ETH) |
| `--deposit-max-fee` | `1000000` | Max builder deposit queue fee; deposits and top-ups wait above it (Gwei, 0 = no limit) |

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
