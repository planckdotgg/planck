.PHONY: build test install install-no-launch

ifeq ($(OS),Windows_NT)
INSTALL = powershell -ExecutionPolicy Bypass -File scripts/install-windows.ps1
INSTALL_NO_LAUNCH = $(INSTALL)
else ifeq ($(shell uname -s),Darwin)
INSTALL = ./scripts/install-macos.sh
INSTALL_NO_LAUNCH = ./scripts/install-macos.sh --no-launch
else
INSTALL = @echo "install is supported on macOS and Windows only"; exit 1
INSTALL_NO_LAUNCH = $(INSTALL)
endif

build:
	CGO_ENABLED=0 go build ./cmd/planck-client ./cmd/planck-host

test:
	go test ./...

install:
	$(INSTALL)

install-no-launch:
	$(INSTALL_NO_LAUNCH)
