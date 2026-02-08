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
   - **Bids Won Store**: In-memory tracking of successfully delivered blocks
     - Thread-safe circular buffer (1000 entries max, ~200KB memory)
     - Stores: slot, block hash, transaction count, blob count, value (ETH/wei), timestamp
     - Populated after successful block publication in `submitBlindedBlockV2`
     - Pagination support via `/api/buildoor/bids-won` endpoint

6. **WebUI** (`pkg/webui/`)
   - React/TypeScript dashboard
   - Real-time event stream via Server-Sent Events (SSE)
   - Visual slot timeline, bid tracking, validator registrations
   - Configuration updates via HTTP API
   - **Bids Won View**: Paginated table of successfully delivered blocks
     - Tab navigation: Dashboard / Bids Won
     - Real-time updates when new blocks are delivered
     - Click-to-copy block hashes, relative timestamps
     - Shows: slot, block hash, # transactions, # blobs, value in ETH

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
│   │   ├── bids_won.go    # BidsWonStore and BidWonEntry types
│   │   └── ...
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
- `GET /api/buildoor/bids-won` - Paginated list of successfully delivered blocks
  - Query params: `offset` (default: 0), `limit` (default: 20, max: 100)
  - Returns: `{ bids_won: [], total: number, offset: number, limit: number }`
- `GET /api/buildoor/builder-api-status` - Builder API configuration and validator count

**Real-time events via SSE** (`/api/events`):
- `bid_won` - Emitted when a block is successfully delivered via Builder API
  - Data: slot, block_hash, num_transactions, num_blobs, value_eth, value_wei, timestamp
  - Auto-refreshes first page of Bids Won table
  - Logged in event stream for debugging

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

### Bids Won Feature (Builder API)

The Bids Won feature tracks successfully delivered blocks via the Builder API, providing visibility into block production outcomes.

**Backend Architecture:**
- **BidsWonStore** (`pkg/builderapi/bids_won.go`):
  - Thread-safe circular buffer with `sync.RWMutex`
  - Fixed capacity: 1000 entries (oldest evicted on overflow)
  - Memory footprint: ~200KB
  - Stores newest first for efficient pagination
  - Entry fields: slot, block_hash, num_transactions, num_blobs, value_eth, value_wei, timestamp

- **Integration Point** (`pkg/builderapi/server.go`):
  - Data captured in `handleSubmitBlindedBlockV2` after successful `SubmitFuluBlock()`
  - Extracts transaction count from `event.Payload.Transactions`
  - Extracts blob count from `event.BlobsBundle.Commitments`
  - Converts block value from wei to ETH using `weiToETH()` (18 decimal precision)
  - Broadcasts `bid_won` event to WebUI for real-time updates

- **REST API** (`pkg/webui/handlers/api/api.go`):
  - Endpoint: `GET /api/buildoor/bids-won?offset=0&limit=20`
  - Offset-based pagination (simpler than cursor for bounded dataset)
  - Returns: `BidsWonResponse` with entries array, total count, offset, limit
  - Gracefully handles nil builderAPISvc (returns empty array)

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
- **Memory Management**: Circular buffer prevents unbounded growth
- **Pagination Strategy**: Offset-based (not cursor) suitable for 1000-entry cap
- **Real-time Updates**: Only first page refreshes automatically to avoid offset confusion
- **Thread Safety**: RWMutex allows concurrent reads during pagination
- **Value Precision**: Store both wei (uint64 for sorting) and ETH string (for display)
- **ePBS Compatibility**: Uses "Bids Won" terminology (not "Payloads Delivered")

**Testing the Feature:**
1. Enable Builder API: `--builder-api-enabled --builder-api-port 18550`
2. Submit a blinded block via `POST /eth/v2/builder/blinded_blocks`
3. Check backend logs for "Bid won" entry
4. Open WebUI, click "Bids Won" tab
5. Verify table shows: slot, block hash, transaction count, blob count, value
6. Submit another block while on first page → should auto-refresh

## Common Issues

1. **"builder-privkey is required"**: Must provide BLS private key (64 hex chars without 0x prefix)
2. **"failed to connect to EL engine API"**: Check JWT secret file path and Engine API URL
3. **Payload building fails**: Ensure `--payload-build-time` is sufficient (default 500ms)
4. **Bids rejected**: Check ePBS timing flags align with network's slot timing
5. **Builder API signature failures**: Genesis parameters are automatically fetched from beacon node; verify beacon node is accessible
