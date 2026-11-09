GO ?= go
# The version of a release, the tag without "v"; there is no default. release-check, release, sdk-tag and gh-release
# need it. The tag is the only source of the version, for edelweissd, the JS SDK tarball and the Go SDK's tag
# sdk/v<version> (the Go SDK reports the version of its module from the build info of the extension binary). build puts
# VERSION into edelweissd and sdk-pack into the tarball when it is set.
VERSION ?=
# The placeholder version of the sources: the default of core/internal/version, the version of web/sdk/package.json and
# the exact @edelweiss/sdk pin of web/shell/package.json. Without a VERSION, build and sdk-pack name their files after
# it, and edelweissd and the tarball report it.
DEV_VERSION := 0.0.0-dev
# The version of the files that build and sdk-pack write.
BUILD_VERSION = $(or $(VERSION),$(DEV_VERSION))
# The fixed version of the release files that make test-service builds (edelweissd and the system extension): they have
# no tag there.
TEST_VERSION := 0.0.0-test
# The files of a release in dist/, besides checksums.txt: make release writes them, make gh-release uploads exactly these.
RELEASE_FILES = edelweissd_$(VERSION)_linux_amd64 edelweissd_$(VERSION)_linux_arm64 edelweiss-sdk-$(VERSION).tgz
GO_PKGS := ./sdk/... ./core/... ./dev/mock-github/...
GO_DIRS := sdk core dev
ACTIONLINT := github.com/rhysd/actionlint/cmd/actionlint@v1.7.12

LDFLAGS = -s -w $(if $(VERSION),-X github.com/Pukka922/edelweiss/core/internal/version.Version=$(VERSION))

# A VERSION goes into the shell lines and the file names of build-go, checksums and sdk-pack, so it has to pass
# release-check first, however they are started (make build VERSION=..., an exported VERSION, the MAKEFLAGS of a
# calling make); without a VERSION they need none (CI runs make lint test and make sdk-pack-check without one).
VERSION_CHECK = $(if $(VERSION),release-check)

EMBED_DIR := core/internal/webui/dist

# No target of this Makefile runs at the same time as another, also with make -j: edelweissd embeds the shell that
# embed-shell copies, checksums reads the binaries of build-go, and the web build (npm run build of the workspaces) and
# sdk-pack (build:package) both write web/sdk/dist. The prerequisites below say the first two as well, so that
# make build-go or make checksums on their own are right too.
.NOTPARALLEL:

# The checkout of github.com/Pukka922/edelweiss-ext-system next to this repository: make dev and make test-service
# build the system extension there, with its own Makefile and against the SDKs of this repository.
EXT_SYSTEM_DIR ?= ../edelweiss-ext-system
EXT_SYSTEM_ABS = $(abspath $(EXT_SYSTEM_DIR))
# make dev DOCKER=1 adds a Docker daemon of its own for edelweiss-ext-docker (the compose profile docker).
DOCKER ?=
COMPOSE_BASE = EXT_SYSTEM_DIR=$(EXT_SYSTEM_ABS) EDELWEISS_DEV_DOCKER=$(DOCKER) docker compose -f dev/compose.yaml
COMPOSE = $(COMPOSE_BASE)$(if $(filter 1,$(DOCKER)), --profile docker)

.PHONY: test lint lint-workflows fmt build-go test-alpine test-image test-service test-selfupdate web-deps web-test web-lint web \
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

build-go: $(VERSION_CHECK) embed-shell
	mkdir -p dist
	for arch in amd64 arm64; do \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch $(GO) build -trimpath -ldflags "$(LDFLAGS)" \
			-o dist/edelweissd_$(BUILD_VERSION)_linux_$$arch ./core/cmd/edelweissd || exit 1; \
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

checksums: $(VERSION_CHECK) build-go
	cd dist && sha256sum edelweissd_$(BUILD_VERSION)_linux_* > checksums.txt

# edelweissd for amd64 and arm64 in dist/, with checksums.txt: edelweissd_<VERSION>_linux_<arch>, or without a VERSION
# edelweissd_0.0.0-dev_linux_<arch>, which reports the placeholder version 0.0.0-dev. A VERSION is checked first, before
# npm and the web build.
build: $(VERSION_CHECK) embed-shell build-go checksums

# The JS SDK as dist/edelweiss-sdk-<version>.tgz: JavaScript and type definitions, packed from a staging directory.
# scripts/sdk-pack.mjs takes the version from SDK_PACK_VERSION and writes it into the tarball's package.json: VERSION,
# or without one the placeholder 0.0.0-dev.
sdk-pack: export SDK_PACK_VERSION := $(BUILD_VERSION)
sdk-pack: $(VERSION_CHECK) web-deps
	npm run build:package -w @edelweiss/sdk
	node scripts/sdk-pack.mjs

# Acceptance test of the tarball of make sdk-pack (of VERSION, or of 0.0.0-dev) in a fresh project (needs the npm
# registry).
sdk-pack-check: sdk-pack
	sh scripts/sdk-pack-check.sh dist/edelweiss-sdk-$(BUILD_VERSION).tgz

# The tag is the only source of the version, so release-check compares it with no file (make release puts it into
# edelweissd and the JS SDK tarball). It checks that VERSION is given, and then its format, by Go's rules for a version
# without the v: x.y.z or x.y.z-pre, numbers without leading zeros, no empty pre-release identifier and no leading zero
# in a numeric one, and no +build metadata (Go ignores it, so sdk/v0.2.0+x would be sdk/v0.2.0). The value reaches the
# check through the environment, so the shell does not parse it as code, and the line after it only runs for a plain
# version. That protects the shell lines only: make itself still expands $(...) in a VERSION from the command line, so
# the tag-name check of the release workflow is the real boundary there.
# One pre-release identifier: digits without a leading zero, or characters of 0-9A-Za-z- with a letter or a hyphen.
SEMVER_ID := (0|[1-9][0-9]*|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*)
SEMVER_RE := ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-$(SEMVER_ID)(\.$(SEMVER_ID))*)?$$
release-check: export RELEASE_CHECK_VERSION := $(VERSION)
release-check:
	@test -n "$$RELEASE_CHECK_VERSION" || { \
		echo "release-check: VERSION is required: the version of the release, the tag without v (make release VERSION=0.2.0)" >&2; \
		exit 1; }
	@export LC_ALL=C; bad=; \
	case "$$RELEASE_CHECK_VERSION" in \
		""|*[!0-9A-Za-z.-]*) bad=1 ;; \
		*) printf '%s\n' "$$RELEASE_CHECK_VERSION" | grep -Eq '$(SEMVER_RE)' || bad=1 ;; \
	esac; \
	test -z "$$bad" || { \
		printf 'release-check: VERSION=%s is not a version like 0.1.0 or 0.2.0-rc.1 (no v prefix, no +build metadata, no leading zeros)\n' \
			"$$RELEASE_CHECK_VERSION" >&2; exit 1; }
	@echo "release-check: $(VERSION) OK"

# The release files in dist/, all of which the release workflow uploads: edelweissd for amd64 and arm64 and the JS SDK
# tarball, with the version of the tag in them, and checksums.txt over these three.
# Before it writes checksums.txt, it runs edelweissd for this machine with the version subcommand and refuses unless it
# prints exactly VERSION (a binary that fails stops the release too). The other architecture cannot be run here, and a
# machine that is not linux/amd64 or linux/arm64 cannot run either binary; that stops the release instead of letting it
# go out unchecked (the release workflow runs on linux/amd64). The checksums.txt of make build (over edelweissd only) is
# removed before the check, so a refused release leaves no checksums.txt in dist/.
release: release-check
	rm -rf dist
	$(MAKE) build sdk-pack
	rm -f dist/checksums.txt
	@os=$$($(GO) env GOHOSTOS); arch=$$($(GO) env GOHOSTARCH); \
	case "$$os/$$arch" in linux/amd64|linux/arm64) ;; *) \
		echo "release: edelweissd cannot be run on this machine ($$os/$$arch) to check its version" >&2; exit 1 ;; \
	esac; \
	bin=dist/edelweissd_$(VERSION)_linux_$$arch; \
	got=$$($$bin version) || { echo "release: $$bin version failed" >&2; exit 1; }; \
	test "$$got" = "$(VERSION)" || { \
		echo "release: $$bin reports version \"$$got\", expected $(VERSION)" >&2; exit 1; }
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
# release files (make release in EXT_SYSTEM_DIR) in a fresh Alpine container. Neither has a tag here, so both are built
# with the fixed version TEST_VERSION. dev-sdk gets an empty VERSION, so that a VERSION given to test-service does not
# reach the make sdk-pack it runs here: it installs the tarball of the placeholder version of web/sdk/package.json.
test-service: ext-system-dir
	$(MAKE) build VERSION=$(TEST_VERSION)
	$(MAKE) -C $(EXT_SYSTEM_ABS) dev-sdk EDELWEISS_DIR=$(CURDIR) VERSION=
	$(MAKE) -C $(EXT_SYSTEM_ABS) release VERSION=$(TEST_VERSION)
	VERSION=$(TEST_VERSION) EXT_VERSION=$(TEST_VERSION) EXT_DIST=$(EXT_SYSTEM_ABS)/dist ARCH=$(DEV_GOARCH) \
		sh dev/service-test.sh

# A real self-update of edelweissd as an OpenRC service (needs docker and network for apk): edelweissd 0.0.0-test is
# installed in a fresh Alpine container and updates itself from a mock GitHub (dev/mock-github) to 0.0.1, rolls back
# from the fakes 0.0.2 (exits at once) and 0.0.3 (crashes after 5 s), and refuses 0.0.4 (wrong checksum). All files
# are built here with go build into build/selfupdate/, which is removed again, and never with make build, make
# checksums or make release, which write dist/. The shell is embedded first, as in every edelweissd build. The log
# goes to the file TEST_LOG as well when that is set.
SELFUPDATE_DIR := build/selfupdate
VERSION_PKG := github.com/Pukka922/edelweiss/core/internal/version
test-selfupdate: embed-shell
	set -eu; \
	trap 'rm -rf $(SELFUPDATE_DIR)' EXIT; trap 'exit 130' INT; trap 'exit 143' TERM; \
	rm -rf $(SELFUPDATE_DIR); mkdir -p $(SELFUPDATE_DIR)/bin; \
	gb() { CGO_ENABLED=0 GOOS=linux GOARCH=$(DEV_GOARCH) $(GO) build -trimpath -ldflags "$$1" -o "$$2" "$$3"; }; \
	gb "-s -w -X $(VERSION_PKG).Version=0.0.0-test" $(SELFUPDATE_DIR)/bin/edelweissd-0.0.0-test ./core/cmd/edelweissd; \
	gb "-s -w -X $(VERSION_PKG).Version=0.0.1" $(SELFUPDATE_DIR)/bin/edelweissd-0.0.1 ./core/cmd/edelweissd; \
	gb "-s -w -X main.version=0.0.2 -X main.mode=broken" $(SELFUPDATE_DIR)/bin/fake-0.0.2 ./core/internal/fakeedelweissd; \
	gb "-s -w -X main.version=0.0.3 -X main.mode=crashy" $(SELFUPDATE_DIR)/bin/fake-0.0.3 ./core/internal/fakeedelweissd; \
	gb "-s -w -X main.version=0.0.4 -X main.mode=broken" $(SELFUPDATE_DIR)/bin/fake-0.0.4 ./core/internal/fakeedelweissd; \
	gb "-s -w" $(SELFUPDATE_DIR)/bin/mock-github ./dev/mock-github; \
	rel() { ARCH=$(DEV_GOARCH) sh dev/mock-releases.sh core $(SELFUPDATE_DIR)/releases "$$@"; }; \
	rel 0.0.1 $(SELFUPDATE_DIR)/bin/edelweissd-0.0.1; \
	rel 0.0.2 $(SELFUPDATE_DIR)/bin/fake-0.0.2; \
	rel 0.0.3 $(SELFUPDATE_DIR)/bin/fake-0.0.3; \
	rel 0.0.4 $(SELFUPDATE_DIR)/bin/fake-0.0.4 bad; \
	ARCH=$(DEV_GOARCH) sh dev/selfupdate-test.sh

# The system extension's binary is replaced by a rename, so a running edelweissd never sees a half-written file. dev-sdk
# gets an empty VERSION, as in test-service.
dev-build: ext-system-dir web
	mkdir -p build/dev
	CGO_ENABLED=0 GOOS=linux GOARCH=$(DEV_GOARCH) $(GO) build -o build/dev/edelweissd ./core/cmd/edelweissd
	CGO_ENABLED=0 GOOS=linux GOARCH=$(DEV_GOARCH) $(GO) build -o build/dev/mock-github ./dev/mock-github
	$(MAKE) -C $(EXT_SYSTEM_ABS) dev-sdk EDELWEISS_DIR=$(CURDIR) VERSION=
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
