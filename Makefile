BINARY  := repoman
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

# Install into a user binary directory that is normally already on PATH.
# Override with `make install INSTALL_DIR=/somewhere/bin`.
INSTALL_DIR ?= $(HOME)/.local/bin

.PHONY: build install test vet fmt run clean

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/repoman

install:
	@mkdir -p "$(INSTALL_DIR)"
	GOBIN="$(INSTALL_DIR)" go install -trimpath -ldflags "$(LDFLAGS)" ./cmd/repoman
	@case ":$$PATH:" in \
		*":$(INSTALL_DIR):"*) echo "installed $(INSTALL_DIR)/$(BINARY)" ;; \
		*) echo "installed $(INSTALL_DIR)/$(BINARY)"; \
		   echo "warning: $(INSTALL_DIR) is not on your PATH; add it with:"; \
		   echo "  echo 'export PATH=\"$(INSTALL_DIR):\$$PATH\"' >> ~/.zshrc" ;; \
	esac

test:
	go test ./... -count=1

vet:
	go vet ./...

fmt:
	gofmt -l -w . && go mod tidy

run:
	go run ./cmd/repoman $(ARGS)

clean:
	rm -f $(BINARY)
