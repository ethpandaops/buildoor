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
	if [ ! -f pkg/webui/static/bundle/buildoor.js ]; then $(MAKE) build-ui; fi

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

devnet:
	.hack/devnet/run.sh

devnet-run: devnet
	@. .hack/devnet/generated-vars.env && \
	go run main.go run \
		--builder-privkey "04b9f63ecf84210c5366c66d68fa1f5da1fa4f634fad6dfc86178e4d79ff9e59" \
		--cl-client "$${BEACON_API}" \
		--el-engine-api "$${ENGINE_API}" \
		--el-jwt-secret "$${JWT_SECRET}" \
		--el-rpc "$${EXECUTION_API}" \
		--wallet-privkey "04b9f63ecf84210c5366c66d68fa1f5da1fa4f634fad6dfc86178e4d79ff9e59" \
		--api-port 8082 \
		--builder-api-port 9000 \
		--log-level debug

devnet-run-docker: devnet
	docker build --file ./Dockerfile -t buildoor:devnet-run --build-arg userid=$(CURRENT_UID) --build-arg groupid=$(CURRENT_GID) .
	@. .hack/devnet/generated-vars-docker.env && \
	docker run --rm -v $${JWT_SECRET}:/jwtsecret -p 8082:8080 -u $(CURRENT_UID):$(CURRENT_GID) --network kt-buildoor --hostname buildoor -it buildoor:devnet-run \
		run \
		--builder-privkey "607a11b45a7219cc61a3d9c5fd08c7eebd602a6a19a977f8d3771d5711a550f2" \
		--cl-client "$${BEACON_API}" \
		--el-engine-api "$${ENGINE_API}" \
		--el-jwt-secret "/jwtsecret" \
		--el-rpc "$${EXECUTION_API}" \
		--wallet-privkey "04b9f63ecf84210c5366c66d68fa1f5da1fa4f634fad6dfc86178e4d79ff9e59" \
		--api-port 8080 \
		--builder-api-port 9000 \
		--log-level debug

devnet-clean:
	.hack/devnet/cleanup.sh
