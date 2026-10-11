GO ?= go
# The version of this repository's release: edelweissd, the Go SDK (tag sdk/v<version>, sdk.Version in
# sdk/protocol.go) and the JS SDK (web/sdk/package.json, the exact @edelweiss/sdk pin of web/shell and the web/sdk
# entry of package-lock.json, which npm install refreshes).
# make release-check VERSION=<tag without v> compares them.
RELEASE_VERSION := 0.1.0
VERSION ?= $(RELEASE_VERSION)
# The files of a release in dist/, besides checksums.txt: make release writes them, make gh-release uploads exactly these.
RELEASE_FILES = edelweissd_$(VERSION)_linux_amd64 edelweissd_$(VERSION)_linux_arm64 edelweiss-sdk-$(VERSION).tgz
GO_PKGS := ./sdk/... ./core/... ./dev/mock-github/...
GO_DIRS := sdk core dev
ACTIONLINT := github.com/rhysd/actionlint/cmd/actionlint@v1.7.12

LDFLAGS := -s -w -X github.com/Pukka922/edelweiss/core/internal/version.Version=$(VERSION)

EMBED_DIR := core/internal/webui/dist

SDK_VERSION := $(shell sed -n 's/^ *"version": *"\([^"]*\)".*/\1/p' web/sdk/package.json)
GO_SDK_VERSION := $(shell sed -n 's/^[[:space:]]*Version[[:space:]]*=[[:space:]]*"\([^"]*\)".*/\1/p' sdk/protocol.go)
# The shell pins the SDK to its exact version, and the lock file records the version of web/sdk.
SHELL_SDK_PIN := $(shell sed -n 's/^ *"@edelweiss\/sdk": *"\([^"]*\)".*/\1/p' web/shell/package.json)
LOCK_SDK_VERSION := $(shell sed -n '/^    "web\/sdk": {$$/,/^    }/ s/^      "version": "\([^"]*\)".*/\1/p' package-lock.json)

# The checkout of github.com/Pukka922/edelweiss-ext-system next to this repository: make dev and make test-service
# build the system extension there, with its own Makefile and against the SDKs of this repository.
EXT_SYSTEM_DIR ?= ../edelweiss-ext-system
EXT_SYSTEM_ABS = $(abspath $(EXT_SYSTEM_DIR))
EXT_SYSTEM_VERSION = $(shell sed -n 's/^ *"version": *"\([^"]*\)".*/\1/p' $(EXT_SYSTEM_ABS)/manifest.json)
# make dev DOCKER=1 adds a Docker daemon of its own for edelweiss-ext-docker (the compose profile docker).
DOCKER ?=
COMPOSE_BASE = EXT_SYSTEM_DIR=$(EXT_SYSTEM_ABS) EDELWEISS_DEV_DOCKER=$(DOCKER) docker compose -f dev/compose.yaml
COMPOSE = $(COMPOSE_BASE)$(if $(filter 1,$(DOCKER)), --profile docker)

.PHONY: test lint lint-workflows fmt build-go test-alpine test-image test-service web-deps web-test web-lint web \
	embed-shell build checksums sdk-pack sdk-pack-check release-check release sdk-tag gh-release ext-system-dir \
	dev-build dev dev-down dev-logs smoke mock-releases

test: web-test
	$(GO) test -race $(GO_PKGS)
	sh scripts/sdk-tag-test.sh
	sh scripts/release-guards-test.sh

lint: web-lint lint-workflows
	$(GO) vet $(GO_PKGS)
	@test -z "$$(gofmt -l $(GO_DIRS))" || (gofmt -l $(GO_DIRS); echo "gofmt needed"; exit 1)

lint-workflows:
	GOWORK=off $(GO) run $(ACTIONLINT) -shellcheck= -pyflakes=

fmt:
	gofmt -w $(GO_DIRS)

build-go:
	mkdir -p dist
	for arch in amd64 arm64; do \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch $(GO) build -trimpath -ldflags "$(LDFLAGS)" \
			-o dist/edelweissd_$(VERSION)_linux_$$arch ./core/cmd/edelweissd || exit 1; \
	done

node_modules/.package-lock.json: package-lock.json package.json
	npm ci

web-deps: node_modules/.package-lock.json

web-test: web-deps
	npm test

web-lint: web-deps
	npx biome check .
	npm run typecheck

web: web-deps
	npm run build

embed-shell: web
	find $(EMBED_DIR) -mindepth 1 ! -name .gitkeep -exec rm -rf {} +
	cp -r web/shell/dist/. $(EMBED_DIR)/

checksums:
	cd dist && sha256sum edelweissd_$(VERSION)_linux_* > checksums.txt

# edelweissd for amd64 and arm64 in dist/, with checksums.txt.
build: embed-shell build-go checksums

# The JS SDK as dist/edelweiss-sdk-<version>.tgz: JavaScript and type definitions, packed from a staging directory.
sdk-pack: web-deps
	npm run build:package -w @edelweiss/sdk
	node scripts/sdk-pack.mjs

# Acceptance test of the tarball in a fresh project (needs the npm registry).
sdk-pack-check: sdk-pack
	sh scripts/sdk-pack-check.sh dist/edelweiss-sdk-$(SDK_VERSION).tgz

# The first check is the format of VERSION, by Go's rules for a version without the v: x.y.z or x.y.z-pre, numbers without
# leading zeros, no empty pre-release identifier and no leading zero in a numeric one, and no +build metadata (Go
# ignores it, so sdk/v0.2.0+x would be sdk/v0.2.0). The value reaches the check through the environment, so the shell
# does not parse it as code, and the lines after it only run for a plain version. That protects the shell lines only:
# make itself still expands $(...) in a VERSION from the command line, so the tag-name check of the release workflow is
# the real boundary there.
# One pre-release identifier: digits without a leading zero, or characters of 0-9A-Za-z- with a letter or a hyphen.
SEMVER_ID := (0|[1-9][0-9]*|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*)
SEMVER_RE := ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-$(SEMVER_ID)(\.$(SEMVER_ID))*)?$$
release-check: export RELEASE_CHECK_VERSION := $(VERSION)
release-check:
	@export LC_ALL=C; bad=; \
	case "$$RELEASE_CHECK_VERSION" in \
		""|*[!0-9A-Za-z.-]*) bad=1 ;; \
		*) printf '%s\n' "$$RELEASE_CHECK_VERSION" | grep -Eq '$(SEMVER_RE)' || bad=1 ;; \
	esac; \
	test -z "$$bad" || { \
		printf 'release-check: VERSION=%s is not a version like 0.1.0 or 0.2.0-rc.1 (no v prefix, no +build metadata, no leading zeros)\n' \
			"$$RELEASE_CHECK_VERSION" >&2; exit 1; }
	@test "$(VERSION)" = "$(RELEASE_VERSION)" || { \
		echo "release-check: VERSION=$(VERSION), but RELEASE_VERSION in the Makefile is $(RELEASE_VERSION)" >&2; exit 1; }
	@test "$(VERSION)" = "$(SDK_VERSION)" || { \
		echo "release-check: VERSION=$(VERSION), but web/sdk/package.json has version $(SDK_VERSION)" >&2; exit 1; }
	@test "$(VERSION)" = "$(GO_SDK_VERSION)" || { \
		echo "release-check: VERSION=$(VERSION), but sdk/protocol.go has Version \"$(GO_SDK_VERSION)\"" >&2; exit 1; }
	@test "$(VERSION)" = "$(SHELL_SDK_PIN)" || { \
		echo "release-check: VERSION=$(VERSION), but web/shell/package.json pins @edelweiss/sdk $(SHELL_SDK_PIN)" >&2; exit 1; }
	@test "$(VERSION)" = "$(LOCK_SDK_VERSION)" || { \
		echo "release-check: VERSION=$(VERSION), but the web/sdk entry of package-lock.json has version $(LOCK_SDK_VERSION) (npm install refreshes it)" >&2; exit 1; }
	@echo "release-check: $(VERSION) OK"

# The release files in dist/, all of which the release workflow uploads: edelweissd for amd64 and arm64, the JS SDK
# tarball and checksums.txt over these three.
release: release-check
	rm -rf dist
	$(MAKE) build sdk-pack
	cd dist && sha256sum $(RELEASE_FILES) > checksums.txt

# Release workflow only (the publish job, which builds nothing): tag sdk/v<version> on this commit and push it
# (scripts/sdk-tag.sh).
sdk-tag: release-check
	sh scripts/sdk-tag.sh $(VERSION)

# Release workflow only (the publish job, which builds nothing): the GitHub release v<version> with the files of
# make release (needs gh and GH_TOKEN). dist/ has to hold exactly checksums.txt and RELEASE_FILES: the files match
# checksums.txt, which lists exactly them, and nothing else is there (no stale file of another version, no hidden file).
# A version with a pre-release part (a "-", like 0.2.0-rc.1) is published as a pre-release: the installer takes GitHub's
# latest release, which leaves pre-releases out, so an rc is not offered as an update.
# gh release create fails if the release already exists, so a rerun needs the release (or draft) deleted first, never
# the tags.
gh-release: release-check
	@test -f dist/checksums.txt || { echo "gh-release: dist/checksums.txt is missing; run make release first" >&2; exit 1; }
	cd dist && sha256sum -c checksums.txt
	@test "$$(ls -A dist | LC_ALL=C sort)" = "$$(printf '%s\n' checksums.txt $(RELEASE_FILES) | LC_ALL=C sort)" || { \
		echo "gh-release: dist/ has to hold exactly checksums.txt and $(RELEASE_FILES); run make release first" >&2; exit 1; }
	@test "$$(sed 's/^[0-9a-f]\{64\} [ *]//' dist/checksums.txt | LC_ALL=C sort)" = "$$(printf '%s\n' $(RELEASE_FILES) | LC_ALL=C sort)" || { \
		echo "gh-release: dist/checksums.txt has to list exactly $(RELEASE_FILES); run make release first" >&2; exit 1; }
	gh release create "v$(VERSION)" --verify-tag --title "edelweiss $(VERSION)" --generate-notes \
		$(if $(findstring -,$(VERSION)),--prerelease) $(addprefix dist/,checksums.txt $(RELEASE_FILES))

test-image:
	docker build -q -t edelweiss-test:latest -f dev/Dockerfile.test dev

test-alpine: test-image
	docker run --rm -e EDELWEISS_INTEGRATION=1 \
		-v $(CURDIR):/src -v edelweiss-gomod:/root/go/pkg/mod -v edelweiss-gocache:/root/.cache/go-build \
		-w /src edelweiss-test:latest \
		go test -tags integration -count=1 $(GO_PKGS)

# The dev container runs the host's architecture (docker pulls the image for it).
DEV_GOARCH ?= $(shell $(GO) env GOARCH)

ext-system-dir:
	@test -f "$(EXT_SYSTEM_ABS)/manifest.json" -a -f "$(EXT_SYSTEM_ABS)/Makefile" || { \
		echo "the system extension is missing: no edelweiss-ext-system checkout at $(EXT_SYSTEM_ABS) (EXT_SYSTEM_DIR=$(EXT_SYSTEM_DIR))" >&2; \
		echo "clone github.com/Pukka922/edelweiss-ext-system next to this repository or set EXT_SYSTEM_DIR=<path>" >&2; \
		exit 1; }

# OpenRC end to end (needs docker and network for apk): edelweissd from dist/ (make build) and the system extension's
# release files (make release in EXT_SYSTEM_DIR) in a fresh Alpine container.
test-service: ext-system-dir build
	$(MAKE) -C $(EXT_SYSTEM_ABS) dev-sdk EDELWEISS_DIR=$(CURDIR)
	$(MAKE) -C $(EXT_SYSTEM_ABS) release VERSION=$(EXT_SYSTEM_VERSION)
	VERSION=$(VERSION) EXT_VERSION=$(EXT_SYSTEM_VERSION) EXT_DIST=$(EXT_SYSTEM_ABS)/dist ARCH=$(DEV_GOARCH) \
		sh dev/service-test.sh

# The system extension's binary is replaced by a rename, so a running edelweissd never sees a half-written file.
dev-build: ext-system-dir web
	mkdir -p build/dev
	CGO_ENABLED=0 GOOS=linux GOARCH=$(DEV_GOARCH) $(GO) build -o build/dev/edelweissd ./core/cmd/edelweissd
	CGO_ENABLED=0 GOOS=linux GOARCH=$(DEV_GOARCH) $(GO) build -o build/dev/mock-github ./dev/mock-github
	$(MAKE) -C $(EXT_SYSTEM_ABS) dev-sdk EDELWEISS_DIR=$(CURDIR)
	$(MAKE) -C $(EXT_SYSTEM_ABS) build
	cp $(EXT_SYSTEM_ABS)/build/edelweiss-ext-system_linux_$(DEV_GOARCH) build/dev/edelweiss-ext-system.new
	mv -f build/dev/edelweiss-ext-system.new build/dev/edelweiss-ext-system

# The mock GitHub's releases, built from core/internal/testext (dev/mock-releases.sh); they need Go only.
mock-releases:
	ARCH=$(DEV_GOARCH) sh dev/mock-releases.sh

dev: dev-build mock-releases
	$(COMPOSE) up -d --force-recreate
	@echo "edelweiss dev: http://localhost:9090  (admin / edelweiss-admin, user / edelweiss-user)"
	@$(if $(filter 1,$(DOCKER)),echo "  with a Docker daemon of its own for edelweiss-ext-docker (compose service docker)",true)

# with the profile, so that a Docker daemon of make dev DOCKER=1 stops too (its volumes stay)
dev-down:
	$(COMPOSE_BASE) --profile docker down

dev-logs:
	$(COMPOSE) logs -f

smoke:
	sh dev/smoke.sh
