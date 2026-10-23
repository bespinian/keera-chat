GO ?= go
PODMAN ?= podman

# Everything a build writes goes here, so `make clean` removes one directory.
BUILD_DIR ?= build

# Without cgo, like the image.
.PHONY: build
build:
	@mkdir -p $(BUILD_DIR)
	@CGO_ENABLED=0 $(GO) build -trimpath -o $(BUILD_DIR)/keera-chat ./cmd/keera-chat

# Keera Chat under air, which rebuilds and restarts it on every save. It talks
# to a gateway that is already running, by default the gateway's own
# `make dev`. Settings in the environment win over these.
AIR ?= air

.PHONY: dev
dev:
	@command -v $(AIR) >/dev/null 2>&1 || { \
		echo "air is not on PATH. Install it with:"; \
		echo "  go install github.com/air-verse/air@latest"; \
		exit 1; }
	@# A fixed secret, so a restart does not sign you out. It only ever
	@# protects a cookie on this machine.
	@# localhost, not 127.0.0.1, because identity providers often accept only
	@# localhost for the gateway's KEERA_PUBLIC_URL. The browser has to use
	@# the same name for both, or the sign-in cookies get lost on the way
	@# back. The gateway then needs KEERA_CHAT_URL=http://localhost:8090.
	@KEERA_CHAT_GATEWAY_URL="$${KEERA_CHAT_GATEWAY_URL:-http://localhost:8080}" \
		KEERA_CHAT_PUBLIC_URL="$${KEERA_CHAT_PUBLIC_URL:-http://localhost:8090}" \
		KEERA_CHAT_SECRET_KEY="$${KEERA_CHAT_SECRET_KEY:-dev-secret-key-not-for-real-use}" \
		KEERA_CHAT_ADDR="$${KEERA_CHAT_ADDR:-127.0.0.1:8090}" \
		KEERA_CHAT_LOG_FORMAT="$${KEERA_CHAT_LOG_FORMAT:-text}" \
		KEERA_CHAT_LOG_LEVEL="$${KEERA_CHAT_LOG_LEVEL:-debug}" \
		sh -c 'echo; echo "  chat     $$KEERA_CHAT_PUBLIC_URL"; \
			echo "  gateway  $$KEERA_CHAT_GATEWAY_URL"; echo; exec $(AIR)'

.PHONY: check
check: test vet fmt-check

.PHONY: test
test:
	@$(GO) test -race -cover ./...

.PHONY: vet
vet:
	@$(GO) vet ./...

.PHONY: fmt-check
fmt-check:
	@out=$$(gofmt -l cmd internal); if [ -n "$$out" ]; then \
		echo "gofmt would change:"; echo "$$out"; exit 1; fi

.PHONY: fmt
fmt:
	@gofmt -w cmd internal

# golangci-lint is not needed to build or test, so lint is not part of
# `check` and asks for it instead of installing it.
GOLANGCI_LINT ?= golangci-lint

.PHONY: lint
lint:
	@command -v $(GOLANGCI_LINT) >/dev/null 2>&1 || { \
		echo "golangci-lint is not on PATH. Install it with:"; \
		echo "  go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0"; \
		echo "CI pins v2.15, so a newer one may report what CI does not."; \
		exit 1; }
	@$(GOLANGCI_LINT) run ./...

CHAT_IMAGE ?= localhost/keera-chat:latest

# What the image's binary reports. .git is not in the build context, so the
# toolchain cannot stamp it. The release workflow passes the tag.
VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo devel)

.PHONY: image
image:
	@$(PODMAN) build -f Containerfile -t $(CHAT_IMAGE) --build-arg VERSION='$(VERSION)' .

# What is inside the image, for auditors and vulnerability scanners. The image
# is FROM scratch, so this is the Go binary and its modules, which syft reads
# from the build info.
#
# The image is rebuilt first, since an SBOM of whatever was tagged last is
# worse than none. The rebuild is cached.
#
# Pinned like everything else a release is built with, so two builds of one
# commit describe it the same way. Dependabot does not read this file.
SYFT ?= syft
SYFT_IMAGE ?= docker.io/anchore/syft:v1.55.0
SBOM_DIR ?= $(BUILD_DIR)/sbom
# CycloneDX is what the downstream scanners take. For another syft format, such
# as spdx-json, change SBOM_FILE too.
SBOM_FORMAT ?= cyclonedx-json
SBOM_FILE ?= $(SBOM_DIR)/keera-chat.cdx.json

# syft runs from its own image unless it is on PATH. It reads an OCI archive,
# not the image store, so it needs no container socket and works the same
# under podman and docker. In the container it reads /scan and writes /out.
#
# Without --source-name and --source-version, syft would name the SBOM's
# subject after a path under /tmp.
.PHONY: sbom
sbom: image
	@mkdir -p $(dir $(SBOM_FILE))
	@id=$$($(PODMAN) image inspect --format '{{.Id}}' $(CHAT_IMAGE)); \
	out=$$(cd $(dir $(SBOM_FILE)) && pwd); \
	tmp=$$(mktemp -d); trap 'rm -rf "$$tmp"' EXIT INT TERM; \
	$(PODMAN) save --format oci-archive -o "$$tmp/image.tar" $(CHAT_IMAGE) >/dev/null 2>&1; \
	if command -v $(SYFT) >/dev/null 2>&1; then \
		run_syft() { $(SYFT) "$$@"; }; scan=$$tmp; dest=$$out; \
	else \
		run_syft() { $(PODMAN) run --rm -v "$$tmp:/scan:z" -v "$$out:/out:z" $(SYFT_IMAGE) "$$@"; }; \
		scan=/scan; dest=/out; \
	fi; \
	run_syft scan "oci-archive:$$scan/image.tar" \
		--source-name '$(CHAT_IMAGE)' --source-version "sha256:$$id" \
		-o "$(SBOM_FORMAT)=$$dest/$(notdir $(SBOM_FILE))" && \
	echo "wrote $(SBOM_FILE) for $(CHAT_IMAGE) ($$(echo "$$id" | cut -c1-12))"

.PHONY: clean
clean:
	@rm -rf $(BUILD_DIR)
