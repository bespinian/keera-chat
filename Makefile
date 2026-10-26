GO ?= go
PODMAN ?= podman

BUILD_DIR ?= build

.PHONY: build
build:
	@mkdir -p $(BUILD_DIR)
	@CGO_ENABLED=0 $(GO) build -trimpath -o $(BUILD_DIR)/keera-chat ./cmd/keera-chat

# Live reload with air. Needs a running gateway. The environment overrides
# these defaults.
AIR ?= air

.PHONY: dev
dev:
	@command -v $(AIR) >/dev/null 2>&1 || { \
		echo "air is not on PATH. Install it with:"; \
		echo "  go install github.com/air-verse/air@latest"; \
		exit 1; }
	@# A fixed secret, so a restart doesn't sign you out.
	@# localhost, not 127.0.0.1: identity providers often allow only
	@# localhost, and both URLs must match for the cookies to work.
	@# The gateway needs KEERA_CHAT_URL=http://localhost:8090.
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

GOLANGCI_LINT ?= golangci-lint

.PHONY: fmt-check
fmt-check:
	@out=$$(gofmt -l cmd internal); if [ -n "$$out" ]; then \
		echo "gofmt would change:"; echo "$$out"; exit 1; fi

# gofumpt, like lint, when golangci-lint is on PATH.
.PHONY: fmt
fmt:
	@if command -v $(GOLANGCI_LINT) >/dev/null 2>&1; then $(GOLANGCI_LINT) fmt; \
		else gofmt -w cmd internal; fi

# Not part of `check`, since golangci-lint is optional.
.PHONY: lint
lint:
	@command -v $(GOLANGCI_LINT) >/dev/null 2>&1 || { \
		echo "golangci-lint is not on PATH. Install it with:"; \
		echo "  go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0"; \
		echo "CI pins v2.15, so a newer one may report what CI does not."; \
		exit 1; }
	@$(GOLANGCI_LINT) run ./...

CHAT_IMAGE ?= localhost/keera-chat:latest

# Passed in, since .git is not in the build context.
VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo devel)

.PHONY: image
image:
	@$(PODMAN) build -f Containerfile -t $(CHAT_IMAGE) --build-arg VERSION='$(VERSION)' .

# SBOM of the image. It rebuilds the image first (cached), so it never
# describes a stale one. Pinned for reproducible output. Dependabot doesn't
# read this file.
SYFT ?= syft
SYFT_IMAGE ?= docker.io/anchore/syft:v1.55.0
SBOM_DIR ?= $(BUILD_DIR)/sbom
# For another format, change SBOM_FILE too.
SBOM_FORMAT ?= cyclonedx-json
SBOM_FILE ?= $(SBOM_DIR)/keera-chat.cdx.json

# Uses syft from PATH, else its image. It scans an OCI archive, so it needs
# no container socket. The --source flags stop it naming the SBOM after a
# temp path.
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

# pdf.js renders attached PDFs as images. Vendored, since there is no build
# step. Bump PDFJS_VERSION by hand.
PDFJS_VERSION ?= 6.4.299
PDFJS_DIR ?= internal/webui/assets/vendor/pdfjs

.PHONY: pdfjs
pdfjs:
	@tmp=$$(mktemp -d); trap 'rm -rf "$$tmp"' EXIT INT TERM; \
	curl -fsSL https://registry.npmjs.org/pdfjs-dist/-/pdfjs-dist-$(PDFJS_VERSION).tgz | tar xz -C "$$tmp" && \
	mkdir -p $(PDFJS_DIR) && \
	cp "$$tmp/package/build/pdf.min.mjs" "$$tmp/package/build/pdf.worker.min.mjs" \
		"$$tmp/package/LICENSE" $(PDFJS_DIR)/ && \
	echo "vendored pdf.js $(PDFJS_VERSION) in $(PDFJS_DIR)"

.PHONY: clean
clean:
	@rm -rf $(BUILD_DIR)
