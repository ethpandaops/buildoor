# buildoor - Testing-focused ePBS block builder
# Build:  docker build -t buildoor .
# Run:   docker run --rm buildoor run --builder-privkey 0x... --cl-client http://... --el-engine-api http://... --el-jwt-secret /path/to/jwt
#        (mount config or pass flags; Builder API exposed on 9000 by default)
#
# Build stage: CGO required for herumi BLS
FROM golang:1.25-bookworm AS builder

WORKDIR /app

# Install build deps for CGO (herumi/bls-eth-go-binary)
RUN apt-get update && apt-get install -y --no-install-recommends \
    gcc \
    libc6-dev \
    && rm -rf /var/lib/apt/lists/*

# Copy dependency manifests first for better layer caching
COPY go.mod go.sum ./

# Download modules (including replace directives)
RUN go mod download

# Copy source
COPY . .

# Build with CGO and version ldflags (match Makefile)
ARG VERSION=dev
ARG BUILDTIME
RUN CGO_ENABLED=1 GOOS=linux go build -v \
    -ldflags="-s -w -X github.com/ethpandaops/buildoor/version.BuildVersion=${VERSION} -X github.com/ethpandaops/buildoor/version.BuildTime=${BUILDTIME:-unknown}" \
    -o /buildoor \
    .

# Runtime stage
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# Non-root user
RUN useradd -r -s /bin/false buildoor
USER buildoor

COPY --from=builder /buildoor /usr/local/bin/buildoor

# Builder API (getHeader, submitBlindedBlockV2, validators); default port 9000
EXPOSE 9000

ENTRYPOINT ["buildoor"]
CMD ["run"]
