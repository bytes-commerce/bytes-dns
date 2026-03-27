# Makefile for bytes-dns
# Requires: Go >= 1.22

BINARY     := bytes-dns
CMD        := ./cmd/bytes-dns
VERSION    := $(shell git describe --tags --always 2>/dev/null || echo dev)
COMMIT     := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS    := -s -w \
              -X main.Version=$(VERSION) \
              -X main.Commit=$(COMMIT) \
              -X main.BuildDate=$(BUILD_DATE)

# Default build: native OS/arch, statically linked.
.PHONY: build
build:
	CGO_ENABLED=0 go build -ldflags="$(LDFLAGS)" -o $(BINARY) $(CMD)

# Build for all supported release targets.
.PHONY: build-all
build-all: build-linux-amd64 build-linux-arm64 build-linux-arm

build-linux-amd64:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="$(LDFLAGS)" \
		-o dist/$(BINARY)-linux-amd64 $(CMD)

build-linux-arm64:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="$(LDFLAGS)" \
		-o dist/$(BINARY)-linux-arm64 $(CMD)

build-linux-arm:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -ldflags="$(LDFLAGS)" \
		-o dist/$(BINARY)-linux-armv7 $(CMD)

.PHONY: dist
dist:
	mkdir -p dist
	$(MAKE) build-linux-amd64
	$(MAKE) build-linux-arm64
	$(MAKE) build-linux-arm
	@echo "Binaries in dist/:"
	@ls -lh dist/

# Run tests.
.PHONY: test
test:
	go test ./... -v -count=1

# Lint via staticcheck (install: go install honnef.co/go/tools/cmd/staticcheck@latest).
.PHONY: lint
lint:
	go vet ./...
	@command -v staticcheck &>/dev/null && staticcheck ./... || echo "staticcheck not installed - skipping"

# Format code.
.PHONY: fmt
fmt:
	gofmt -w .

# Install locally (requires sudo for /usr/local/bin).
.PHONY: install
install: build
	sudo install -m 755 $(BINARY) /usr/local/bin/$(BINARY)
	@echo "Installed /usr/local/bin/$(BINARY)"

# Full install via install.sh (systemd integration).
.PHONY: install-service
install-service:
	sudo bash install.sh

.PHONY: uninstall
uninstall:
	sudo bash uninstall.sh

.PHONY: clean
clean:
	rm -f $(BINARY)
	rm -rf dist/

.PHONY: version
version:
	@echo "Version: $(VERSION)"
	@echo "Commit:  $(COMMIT)"
	@echo "Date:    $(BUILD_DATE)"
