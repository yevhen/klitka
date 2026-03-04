SHELL := /bin/bash

.PHONY: help guest guest-base test test-go test-sdk test-nix test-strict-pf-vm test-strict-pf-vm-tart

help:
	@echo "Targets:"
	@echo "  guest       Build guest image (default Alpine)"
	@echo "  guest-base  Build guest image from BASE_ROOTFS (custom rootfs tar)"
	@echo "  test-go     Run Go tests"
	@echo "  test-sdk    Run SDK tests"
	@echo "  test        Build guest + run Go + SDK tests"
	@echo "  test-nix    Run tests inside nix develop"
	@echo "  test-strict-pf-vm  Run strict PF validation inside current dedicated macOS VM"
	@echo "  test-strict-pf-vm-tart  Provision/run dedicated macOS VM via Tart and execute strict PF validation"


guest:
	./guest/image/build.sh


guest-base:
	@if [ -z "$(BASE_ROOTFS)" ]; then echo "BASE_ROOTFS is required"; exit 1; fi
	BASE_ROOTFS="$(BASE_ROOTFS)" ./guest/image/build.sh


test-go:
	go test ./...


test-sdk:
	cd sdk && npm test


test:
	$(MAKE) guest
	$(MAKE) test-go
	$(MAKE) test-sdk


test-nix:
	@if git rev-parse --is-inside-work-tree >/dev/null 2>&1 && git ls-files --error-unmatch flake.nix >/dev/null 2>&1; then \
		nix develop --command bash -c "./guest/image/build.sh && go test ./... && (cd sdk && npm test)"; \
	else \
		nix-shell --run "./guest/image/build.sh && go test ./... && (cd sdk && npm test)"; \
	fi


test-strict-pf-vm:
	./scripts/run-strict-pf-vm.sh --focused


test-strict-pf-vm-tart:
	./scripts/run-strict-pf-vm-tart.sh
