# buildoor
BUILDTIME := $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
VERSION := $(shell git rev-parse --short HEAD)

GOLDFLAGS += -X 'github.com/ethpandaops/buildoor/version.BuildVersion="$(VERSION)"'
GOLDFLAGS += -X 'github.com/ethpandaops/buildoor/version.BuildTime="$(BUILDTIME)"'
GOLDFLAGS += -X 'github.com/ethpandaops/buildoor/version.BuildRelease="$(RELEASE)"'

.PHONY: all test clean

all: docs build

test:
	go test ./...

build: ensure-ui
	@echo version: $(VERSION)
	env CGO_ENABLED=1 go build -v -o bin/ -ldflags="-s -w $(GOLDFLAGS)" .

ensure-ui:
	if [ ! -f pkg/webui/static/index.html ]; then $(MAKE) build-ui; fi

build-ui:
	$(MAKE) -C pkg/webui install
	$(MAKE) -C pkg/webui build

docs:
	go install github.com/swaggo/swag/cmd/swag@v1.16.3 && swag init -g handler.go -d pkg/webui/handlers/api --parseDependency -o pkg/webui/handlers/docs

clean:
	rm -f bin/*
	$(MAKE) -C pkg/webui clean

# Docker
docker:
	docker build --build-arg VERSION="$(VERSION)" --build-arg BUILDTIME="$(BUILDTIME)" -t buildoor:$(VERSION) .
docker-run: docker
	docker run --rm -it buildoor:$(VERSION) run --help

# Which canonical buildoor instance to replace (1-based, sorted by service
# name). Set BUILDOOR_SERVICE to target a specific instance by name instead.
BUILDOOR_INDEX ?= 1
BUILDOOR_SERVICE ?=

devnet:
	BUILDOOR_INDEX=$(BUILDOOR_INDEX) BUILDOOR_SERVICE=$(BUILDOOR_SERVICE) .hack/devnet/run.sh

# Run the replacement locally. A socat sidecar claims the replaced instance's
# service name on the kurtosis network and forwards its builder API port to the
# local process, so the CL's builder API calls reach us.
devnet-run: devnet
	@. .hack/devnet/generated-vars.env && \
	docker rm -f buildoor-devnet-proxy > /dev/null 2>&1 || true && \
	GATEWAY_IP=$$(docker network inspect "$${DOCKER_NETWORK}" --format '{{(index .IPAM.Config 0).Gateway}}') && \
	docker run -d --rm --name buildoor-devnet-proxy \
		--network "$${DOCKER_NETWORK}" --network-alias "$${BUILDOOR_SERVICE}" \
		alpine/socat tcp-listen:8080,fork,reuseaddr tcp:$${GATEWAY_IP}:8086 > /dev/null && \
	trap 'docker rm -f buildoor-devnet-proxy > /dev/null 2>&1' EXIT INT TERM && \
	go run main.go run \
		--builder-mnemonic "$${BUILDER_MNEMONIC}" \
		--builder-key-index "$${BUILDER_KEY_INDEX}" \
		--builder-privkey "$${BUILDER_PRIVKEY}" \
		--cl-client "$${BEACON_API}" \
		--el-engine-api "$${ENGINE_API}" \
		--el-jwt-secret "$${JWT_SECRET}" \
		--el-rpc "$${EXECUTION_API}" \
		--wallet-privkey "$${WALLET_PRIVKEY}" \
		--api-port 8086 \
		--builder-api-url "$${BUILDER_API_URL}" \
		--extra-data "$${EXTRA_DATA}" \
		--validator-ranges-file "$${VALIDATOR_RANGES_FILE}" \
		--builder-api-enabled \
		--epbs-enabled \
		--lifecycle \
		--log-level debug

# Run the replacement as a container on the kurtosis network, claiming the
# replaced instance's service name so the CL's builder API calls reach it.
devnet-run-docker: devnet
	docker build --file ./Dockerfile -t buildoor:devnet-run .
	@. .hack/devnet/generated-vars-docker.env && \
	docker run --rm -v "$${JWT_SECRET}:/jwtsecret" \
		$${VALIDATOR_RANGES_FILE:+-v "$${VALIDATOR_RANGES_FILE}:/validator-ranges.yaml"} \
		-p 8086:8080 \
		--network "$${DOCKER_NETWORK}" \
		--network-alias "$${BUILDOOR_SERVICE}" \
		--hostname "$${BUILDOOR_SERVICE}" \
		-it buildoor:devnet-run \
		run \
		--builder-mnemonic "$${BUILDER_MNEMONIC}" \
		--builder-key-index "$${BUILDER_KEY_INDEX}" \
		--builder-privkey "$${BUILDER_PRIVKEY}" \
		--cl-client "$${BEACON_API}" \
		--el-engine-api "$${ENGINE_API}" \
		--el-jwt-secret "/jwtsecret" \
		--el-rpc "$${EXECUTION_API}" \
		--wallet-privkey "$${WALLET_PRIVKEY}" \
		--api-port 8080 \
		--builder-api-url "$${BUILDER_API_URL}" \
		--extra-data "$${EXTRA_DATA}" \
		$${VALIDATOR_RANGES_FILE:+--validator-ranges-file /validator-ranges.yaml} \
		--builder-api-enabled \
		--epbs-enabled \
		--lifecycle \
		--log-level debug

devnet-clean:
	.hack/devnet/cleanup.sh
