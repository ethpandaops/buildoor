# dora
BUILDTIME := $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
VERSION := $(shell git rev-parse --short HEAD)

GOLDFLAGS += -X 'github.com/ethpandaops/buildoor/version.BuildVersion="$(VERSION)"'
GOLDFLAGS += -X 'github.com/ethpandaops/buildoor/version.BuildTime="$(BUILDTIME)"'
GOLDFLAGS += -X 'github.com/ethpandaops/buildoor/version.BuildRelease="$(RELEASE)"'

.PHONY: all test clean

all: docs build

test:
	go test ./...

build:
	@echo version: $(VERSION)
	env CGO_ENABLED=1 go build -v -o bin/ -ldflags="-s -w $(GOLDFLAGS)" .

docs:
	go install github.com/swaggo/swag/cmd/swag@v1.16.3 && swag init -g handler.go -d pkg/webui/handlers/api --parseDependency -o pkg/webui/handlers/docs

clean:
	rm -f bin/*
	$(MAKE) -C ui-package clean

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
		--log-level debug

devnet-clean:
	.hack/devnet/cleanup.sh
