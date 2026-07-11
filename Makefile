VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  = -s -w -X github.com/slipstream-panel/slipstream/internal/version.Version=$(VERSION)

.PHONY: build ui test vet dist clean

build: ui
	mkdir -p bin
	go build -ldflags '$(LDFLAGS)' -o bin/panel-api ./cmd/panel-api
	go build -ldflags '$(LDFLAGS)' -o bin/panel-agent ./cmd/panel-agent
	go build -ldflags '$(LDFLAGS)' -o bin/slipctl ./cmd/slipctl

ui:
	cd ui && npm ci --silent && npm run build --silent

test: vet
	go test ./...

vet:
	go vet ./...

dist: ui
	mkdir -p dist
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags '$(LDFLAGS)' -o dist/panel-api ./cmd/panel-api
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags '$(LDFLAGS)' -o dist/panel-agent ./cmd/panel-agent
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags '$(LDFLAGS)' -o dist/slipctl ./cmd/slipctl

clean:
	rm -rf bin dist ui/dist
