# Local / CI-parity targets for timetravel
.PHONY: all build test vet lint fmt tidy clean install cross release-dry

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
BIN     := timetravel

ifeq ($(OS),Windows_NT)
  BIN := timetravel.exe
endif

all: vet test build

build:
	go build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN) ./cmd/timetravel

test:
	go test -race -count=1 -timeout 5m ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

lint: vet
	@command -v staticcheck >/dev/null 2>&1 || go install honnef.co/go/tools/cmd/staticcheck@2024.1.1
	staticcheck ./...

tidy:
	go mod tidy

install:
	go install -trimpath -ldflags="$(LDFLAGS)" ./cmd/timetravel

cross:
	@mkdir -p dist
	@for t in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do \
	  GOOS=$${t%/*}; GOARCH=$${t#*/}; EXT=""; \
	  [ "$$GOOS" = "windows" ] && EXT=".exe"; \
	  OUT="dist/timetravel-$$GOOS-$$GOARCH$$EXT"; \
	  echo "$$OUT"; \
	  GOOS=$$GOOS GOARCH=$$GOARCH CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o "$$OUT" ./cmd/timetravel; \
	done

clean:
	rm -f timetravel timetravel.exe
	rm -rf dist/

# Local dry-run of the release archive step (linux host)
release-dry: cross
	@echo "Built version $(VERSION) in dist/"