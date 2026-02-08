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
go test ./pkg/builder

# Run tests with verbose output
go test -v ./pkg/builder

# Run specific test
go test -v ./pkg/builder -run TestPayloadBuilder
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

1. **Builder Service** (`pkg/builder/`)
   - Main orchestrator for payload building
   - Subscribes to beacon node's `payload_attributes` events
   - Schedules builds at configurable times relative to slot start
   - Calls Engine API to construct execution payloads (forkchoiceUpdated → getPayload)
   - Emits `PayloadReadyEvent` to subscribers

2. **ePBS Service** (`pkg/epbs/`)
   - Time-scheduled bidding and payload reveals
   - Subscribes to Builder's `PayloadReadyEvent`
   - 10ms tick processor for precise timing control
   - Tracks bid competition and payload inclusion
   - Key components: Scheduler, BidCreator, RevealHandler, BidTracker

3. **Chain Service** (`pkg/chain/`)
   - Manages epoch-level beacon state
   - Caches last 2 epochs of state
   - Detects fork transitions (Electra → Gloas)
   - Loads builder registrations from beacon state (post-Gloas)
   - Provides slot↔timestamp conversions

4. **Lifecycle Manager** (`pkg/lifecycle/`)
   - Builder registration on beacon chain
   - Balance monitoring and auto top-ups
   - Deposit and exit operations
   - Optional component (only active with `--lifecycle` flag)

5. **Builder API Server** (`pkg/builderapi/`)
   - Traditional Builder API (pre-ePBS mode)
   - Endpoints: registerValidator, getHeader, submitBlindedBlock
   - Supports Fulu fork's split header/payload model
   - Validator fee recipient management

6. **WebUI** (`pkg/webui/`)
   - React/TypeScript dashboard
   - Real-time event stream via Server-Sent Events (SSE)
   - Visual slot timeline, bid tracking, validator registrations
   - Configuration updates via HTTP API

### Event Flow

```
Beacon Node Event → Builder → ePBS → Beacon Node
       ↓              ↓         ↓
 payload_attributes  Build   Submit Bids
                      ↓         ↓
                 PayloadReady  Reveal
```

### Data Flow (ePBS Mode)

1. Beacon node emits `payload_attributes` event
2. Builder validates slot against schedule
3. Builder schedules build at `BuildStartTime` (relative to slot)
4. PayloadBuilder calls Engine API (forkchoiceUpdated → getPayload)
5. Builder emits `PayloadReadyEvent` to subscribers
6. ePBS Service receives event and stores payload
7. ePBS Scheduler ticks every 10ms:
   - Submits bids between `BidStartTime` and `BidEndTime`
   - Reveals payload at `RevealTime`
8. Head event received → check if our payload was included

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
- **Bidding**: `--epbs-bid-min`, `--epbs-bid-increase`, `--epbs-bid-interval`

### Startup Sequence

The application initializes services in this order (see `cmd/run.go`):
1. CL client connection
2. Engine API connection
3. BLS signer initialization
4. Chain Service start
5. Lifecycle Manager initialization (if enabled)
6. Builder Service initialization
7. ePBS Service initialization (if enabled)
8. Builder API Server start (if enabled)
9. WebUI HTTP server start (if APIPort > 0)
10. Service starts (Lifecycle → Builder → ePBS)

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

## Code Structure

```
buildoor/
├── cmd/                    # CLI commands (root, run, deposit, exit)
├── pkg/
│   ├── builder/           # Core payload building logic
│   ├── builderapi/        # Traditional Builder API server
│   ├── chain/             # Beacon state management
│   ├── config/            # Configuration types and defaults
│   ├── epbs/              # ePBS bidding and revealing
│   ├── lifecycle/         # Deposit/exit/balance management
│   ├── rpc/
│   │   ├── beacon/        # Beacon node client
│   │   ├── engine/        # Engine API client
│   │   └── execution/     # Execution RPC client
│   ├── signer/            # BLS signing utilities
│   ├── utils/             # Shared utilities (Dispatcher, etc.)
│   ├── wallet/            # ECDSA wallet for transactions
│   └── webui/             # HTTP server and React frontend
│       ├── handlers/      # HTTP API handlers
│       ├── src/           # React TypeScript source
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

To make frontend changes:
1. `cd pkg/webui && npm run dev` (watch mode)
2. Backend serves from `static/` automatically
3. Rebuild binary only if adding new API endpoints

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

## Common Issues

1. **"builder-privkey is required"**: Must provide BLS private key (64 hex chars without 0x prefix)
2. **"failed to connect to EL engine API"**: Check JWT secret file path and Engine API URL
3. **Payload building fails**: Ensure `--payload-build-time` is sufficient (default 500ms)
4. **Bids rejected**: Check ePBS timing flags align with network's slot timing
5. **Builder API signature failures**: Genesis parameters are automatically fetched from beacon node; verify beacon node is accessible
