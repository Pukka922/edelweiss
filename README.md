# edelweiss

> **Note:** This project is vibe coded, for lack of time, and made only for my personal use: to manage my Raspberry Pi
> more comfortably. Anyone is welcome to use it, but at their own risk.

A web administration interface for Alpine Linux, in the spirit of Cockpit. Its functions are extensions, which can be
installed at runtime: single static binaries with a Go backend and a SolidJS interface, installed from a file or
straight from a GitHub release.

- Sign in with your Linux account; members of `wheel` switch to admin mode, in which extensions run as root.
- One static binary (`edelweissd`), OpenRC service, HTTPS with a self-signed or your own certificate.
- Extensions: the system overview, in its own repository
  [Pukka922/edelweiss-ext-system](https://github.com/Pukka922/edelweiss-ext-system), and the example extension
  [Pukka922/edelweiss-ext-hello](https://github.com/Pukka922/edelweiss-ext-hello), the template for new extensions.
- SDKs for extensions, published with every release: the Go module `github.com/Pukka922/edelweiss/sdk` and the JS SDK
  `@edelweiss/sdk` as `edelweiss-sdk-<version>.tgz`.

**Status:** v0.2, under development.

## Documentation

- [Installing edelweiss on Alpine Linux](docs/install.md); the short version is [Installation](#installation)
- [Developing extensions](docs/extension-development.md), starting from the template
  [edelweiss-ext-hello](https://github.com/Pukka922/edelweiss-ext-hello)

## Building

Requirements: Go 1.26, Node.js 22 with npm, make and a C compiler (`gcc`: `make test` runs the Go tests with the race
detector, which needs cgo); Docker for the dev environment and the integration tests.

```sh
make build                    # web UIs, then static edelweissd binaries for amd64 and arm64 in dist/ (+ checksums.txt)
make test                     # Go (race detector), web tests and the tests of scripts/sdk-tag.sh and the release guards
make lint                     # go vet, gofmt, Biome, TypeScript, actionlint
make sdk-pack                 # the JS SDK as dist/edelweiss-sdk-<version>.tgz
make sdk-pack-check           # installs that tarball in a fresh project, builds, type-checks and tests a UI with it
make release VERSION=0.2.0    # all release files in dist/ (see Releasing)
```

`make sdk-pack-check` needs the npm registry. Without `VERSION`, `make build` and `make sdk-pack` use the placeholder
version `0.0.0-dev` of the sources (`edelweissd_0.0.0-dev_linux_<arch>`, `edelweiss-sdk-0.0.0-dev.tgz`);
`make build VERSION=<version>` builds edelweissd of that version.

## Installation

edelweiss runs on 64-bit Alpine Linux 3.24 with OpenRC. The details of every step are in
[the installation guide](docs/install.md).

### 1. Get the binary

Download `edelweissd_<version>_linux_<arch>` and `checksums.txt` from the
[latest release](https://github.com/Pukka922/edelweiss/releases/latest):

| System                                                                            | `<arch>` |
| --------------------------------------------------------------------------------- | -------- |
| x86_64                                                                            | `amd64`  |
| aarch64 (64-bit ARM, for example a Raspberry Pi 3, 4 or 5 with the aarch64 image) | `arm64`  |

32-bit systems (x86, armv7, armhf) are not supported. You can also build the binaries yourself (Linux or WSL): they
are static and cross-compiled, and `make build` always builds amd64 and arm64.

### 2. Prepare the host

1. Set up Alpine with `setup-alpine`: the network, an NTP client (HTTPS downloads from GitHub and the TLS certificate
   need the correct time), the SSH server and a user account (for example `myUser`), which will be your edelweiss
   login.
2. Decide how the system is stored. A _diskless_ Alpine runs from RAM (the default of the images for boards such as
   the Raspberry Pi); only what `lbu commit` saves survives a reboot, and by default that is `/etc` without
   `/etc/init.d`.
   - Recommended: install Alpine in _sys mode_ on its disk or SD card (for a Raspberry Pi as described in the Alpine
     wiki: [Classic install or sys mode on Raspberry Pi](https://wiki.alpinelinux.org/wiki/Classic_install_or_sys_mode_on_Raspberry_Pi)).
     Then everything is stored as on any other Alpine system.
   - Diskless works too, but needs the `lbu` commands in step 4.

### 3. Install

Copy the binary and `checksums.txt` to the host (for example with `scp`), then, as root on the host, in the directory
with the two files:

```sh
grep linux_<arch> checksums.txt | sha256sum -c    # edelweissd_<version>_linux_<arch>: OK
mkdir -p /usr/local/sbin
install -m 755 edelweissd_<version>_linux_<arch> /usr/local/sbin/edelweissd
edelweissd service install
addgroup myUser wheel                            # admin mode; root itself cannot sign in
rc-update add syslog boot                        # the log, if syslog is not enabled yet
rc-service edelweiss start
```

Then open `https://<host>:9090` and accept the self-signed certificate (its fingerprint is in the log:
`grep 'self-signed' /var/log/messages`). If the browser says the certificate is not yet valid because the clock was
wrong when it was created, stop the service, delete `/var/lib/edelweiss/tls` and start it again.

Extensions are installed in the web interface, under **Settings → Extensions → Install from GitHub** (admin mode),
for example the system overview `Pukka922/edelweiss-ext-system` or the Docker management
`Pukka922/edelweiss-ext-docker`. edelweiss shows the repository, the release, the file and its SHA-256 with a warning,
and installs only after you confirm.

### 4. Diskless mode only

Save the binary, the service, the installed extensions and their data:

```sh
lbu include /usr/local/sbin/edelweissd
lbu include /etc/init.d/edelweiss
lbu include /var/lib/edelweiss
lbu include /var/lib/edelweiss-ext-docker       # the data directory of each extension that has one
lbu commit -d
```

In diskless mode run `lbu commit -d` again after every change: an extension installed, updated or removed (also in the
web interface), a changed configuration or a new edelweissd binary.

### Updating

edelweissd updates itself under **Settings → About** (admin mode). Every signed-in user sees the version, the
architecture and the mode there. In admin mode the page checks the latest release of
[Pukka922/edelweiss](https://github.com/Pukka922/edelweiss) (never a pre-release) and offers an update. Before it
changes anything, it shows the repository, the release, the file and its SHA-256 and asks you to confirm.

- **What is checked:** the file `edelweissd_<version>_linux_<arch>` against the `checksums.txt` of the same release; the
  new binary has to print exactly the offered version (`edelweissd version`); and there has to be free space of three
  times the binary's size plus 16 MiB next to the binary.
- **Everybody is signed out.** Open consoles and uploads break off, and the extensions restart.
- **Rollback:** a detached helper swaps the binary and the init script, restarts the service and waits up to 60 s for
  the new version. 30 s later it asks again; if the new version is not up and stable, it puts the old binary and init
  script back and restarts. The backups (`<binary>.edelweiss-bak` and `/var/lib/edelweiss/self-update/init.edelweiss-bak`)
  stay until the next update.
- **Diskless and data mode:** after a successful update edelweissd runs `lbu commit`. The binary has to be in the lbu
  list (`lbu include /usr/local/sbin/edelweissd`); the dialog warns when it is not.
- **Where:** only when edelweissd runs as the OpenRC service that `edelweissd service install` sets up. Otherwise the
  page says why it cannot update (development build, no OpenRC, read-only binary directory and so on).
- **The result** is in `/var/lib/edelweiss/self-update/result.json`; the helper's log is in the syslog (tag
  `edelweiss`) and in `/var/lib/edelweiss/self-update/helper.log`.

The first update to a version with this page is manual: 0.2.0 and older cannot update themselves. Follow
[Upgrading edelweissd](docs/install.md#operation) once; the page does the rest. The same section is the fallback, and
it tells you how to recover when an update was interrupted by the host itself (a power loss, a reboot). The
extensions page offers updates of the extensions (`edelweissd ext update --all` on the command line).

## Development

The dev environment and `make test-service` use the system overview from a checkout of
[edelweiss-ext-system](https://github.com/Pukka922/edelweiss-ext-system) next to this repository:
`../edelweiss-ext-system`, or another place with `EXT_SYSTEM_DIR=<path>`. Without it they stop with a message that
names the path. They build it with its own Makefile against the SDKs of this repository
(`make dev-sdk EDELWEISS_DIR=<this repository>` there), so nothing is fetched from GitHub.

```sh
git clone https://github.com/Pukka922/edelweiss-ext-system ../edelweiss-ext-system   # once
make dev            # Alpine container on http://localhost:9090 with test users, a mock GitHub and the system extension
make dev DOCKER=1   # the same plus a Docker daemon of its own (docker:29.9.0-dind) for edelweiss-ext-docker
make smoke          # checks the running dev container (login, extensions, admin mode, installer)
make dev-build      # rebuilds the system extension, which the running container restarts (a new edelweissd needs make dev)
make dev-logs       # follows the logs of the dev containers (edelweiss, the mock GitHub; DOCKER=1 also the service docker)
make dev-down       # stops the dev containers (also the Docker daemon of DOCKER=1)
npm run watch       # rebuilds the SDK and the shell on change (reload the page)
make mock-releases  # builds the test extension's (core/internal/testext) releases for the mock GitHub
                    # (make dev runs it)
make test-alpine    # Go integration tests as root in Alpine
make test-selfupdate # self-update in a throwaway Alpine container with OpenRC and a mock GitHub: an update, two
                    # rollbacks and a refused checksum (needs docker and network for apk; never touches dist/)
make test-service   # OpenRC service install/uninstall in a fresh Alpine container, with the system extension's release file
                    # (both built with the fixed version 0.0.0-test)
```

- `cd ../edelweiss-ext-system/ui && npm run watch` rebuilds the system extension's UI on change. The dev container
  serves it from there, so reload the page.
- A change to edelweissd itself needs `make dev`.
- The test accounts of the dev container are in `dev/seed-users.sh`.
- The mock GitHub serves `edelweiss/testext` (`v0.1.0`, and `v0.2.0` as the latest release),
  `edelweiss/testext-badsum` (a wrong checksum, which the installer refuses) and `edelweiss/testext-needs-pkg` (it
  requires an Alpine package no system has; the installer refuses it and names the `apk add` command). Try the
  installer with them in admin mode under **Settings → Extensions → Install from GitHub**; `make smoke` checks it with
  them.
- `make dev DOCKER=1` is for [edelweiss-ext-docker](https://github.com/Pukka922/edelweiss-ext-docker): the compose
  service `docker` runs a Docker daemon of its own (the Docker of your machine only runs its container); the dev
  container sees its socket as `/var/run/docker.sock` and has the package `docker` installed, which the extension
  requires. The test account `user` uses Docker through the group `docker`, `admin` only in admin mode. The
  extension's README describes how to install its release file here. Pulled images stay in the volume
  `edelweiss-dev_docker-data`; `docker compose -f dev/compose.yaml --profile docker down -v` removes the volumes.
  - With `DOCKER=1` the dev account `user` has a privileged Docker daemon, as powerful as root on this machine,
    reachable through 127.0.0.1:9090 with the published test passwords.
  - To go back from `DOCKER=1` to a plain `make dev`, run `make dev-down` first; otherwise the Docker daemon keeps
    running.

## Releasing

edelweissd and both SDKs share one version. A release `v<version>` publishes:

- `edelweissd_<version>_linux_amd64` and `edelweissd_<version>_linux_arm64`;
- the JS SDK `edelweiss-sdk-<version>.tgz`;
- `checksums.txt`.

These go up as a GitHub release, and the Go SDK is tagged `sdk/v<version>`, so
`go get github.com/Pukka922/edelweiss/sdk@v<version>` finds it.

No file needs changing for a release: the tag `v<version>` is the only source of the version, for edelweissd, the JS
SDK tarball and the Go SDK's tag `sdk/v<version>`.

1. Check the version with `make release-check VERSION=<version>`. It has to be a plain `x.y.z` or `x.y.z-pre` by the
   rules of Go (no leading zeros, no empty pre-release identifier, no `+build` metadata: Go ignores it in the tag
   `sdk/v<version>`).
2. Push `main`, then the tag, which decides the version: `git push origin main`, then
   `git tag v<version> && git push origin v<version>`.

The sources hold the placeholder version `0.0.0-dev`: `core/internal/version`, `web/sdk/package.json` and the exact
`@edelweiss/sdk` pin of `web/shell/package.json` (and so `package-lock.json`). The release build puts the version of
the tag into edelweissd (`-ldflags -X`) and into the `package.json` of the JS SDK tarball, and checks edelweissd by
running the binary for the build machine with `version`: it has to print exactly that version. A local build without
`VERSION` reports `0.0.0-dev`: `make build` writes `edelweissd_0.0.0-dev_linux_<arch>`, and `make sdk-pack` writes
`edelweiss-sdk-0.0.0-dev.tgz`.

The Go SDK holds no version in its source either: the `sdkVersion` that an extension reports in its manifest is the
version of the module `github.com/Pukka922/edelweiss/sdk` that the extension was built with, read from the build info
of its binary (`0.0.0-dev` when it was built against a local checkout of the SDK, as with `make dev-sdk`).

The release workflow (`.github/workflows/release.yml`) has two jobs, so that the build cannot use the write token.
`build` has a read-only token and keeps no credentials in the checkout. It checks the tag name (`v`, then only letters,
digits, `.`, `+` and `-`), then runs three make targets and hands `dist/` on as a workflow artifact:

- `make release-check`: the tag is a plain `x.y.z` or `x.y.z-pre`;
- `make lint test`: the checks of the CI workflow, so a tag on a commit that fails them publishes nothing;
- `make release`: the files in `dist/`, built from an empty `dist/`, with the version of the tag in edelweissd and in
  the JS SDK tarball.

`publish` runs when `build` has succeeded. It holds the write token, builds nothing and needs neither Go nor Node. It
downloads the artifact into `dist/` and runs two make targets:

- `make sdk-tag`: checks the version again, then creates and pushes `sdk/v<version>` on the same commit. It reads the
  tag of the remote with `git ls-remote`, without fetching. It does nothing if the tag is already there on this
  commit, and it stops if the tag is on another commit or the remote cannot be read;
- `make gh-release`: checks the version again and that `dist/` holds exactly `checksums.txt` and the three files of the
  version, that the files match `checksums.txt` and that it lists exactly them. Then it creates the GitHub release with
  these four files. A version with a `-` (`0.2.0-rc.1`) is published as a GitHub pre-release: the installer takes the
  latest release, which leaves pre-releases out, so an rc is not offered as an update.

A compromised build could still hand over altered files under the expected names: `checksums.txt` shows that the files
are consistent, not where they come from.

The CI workflow (`.github/workflows/ci.yml`) runs `make lint test` and `make sdk-pack-check` (with the tarball of
`0.0.0-dev`) on every push and pull request. The release workflow repeats `make lint test`, but not
`make sdk-pack-check` (it needs the npm registry), so wait for the CI run on `main` before you tag, or run
`make lint test sdk-pack-check` locally first.
`make release VERSION=<version>` builds the same release files locally.

**Published versions are immutable.** Once a release may have been fetched, never move or delete the tags
`v<version>` and `sdk/v<version>`, and never replace or delete the files of the release:

- sum.golang.org records the Go SDK version permanently, so a moved or re-created `sdk/v<version>` makes
  `go get` fail with "checksum mismatch" for everyone, new fetchers included, not only for those who fetched the old
  one;
- the `package-lock.json` files of edelweiss-ext-system and edelweiss-ext-hello pin the integrity of the SDK tarball,
  so a replaced tarball makes `npm ci` fail with `EINTEGRITY`;
- the edelweiss installer refuses a release that changed after the user looked at it (`release_changed`).

A broken release is fixed with a new patch version.

Rerunning a failed workflow is fine as long as the tag `v<version>` stays on the same commit; `make sdk-tag` accepts
`sdk/v<version>` on the same commit. "Re-run failed jobs" repeats only the job that failed, and "Re-run all jobs" builds
again (the upload of the artifact replaces the one of the first attempt). `gh release create` fails if the release
already exists, so if the workflow had already created the GitHub release, delete only the release (never the tags)
before the rerun. Do that only for a release that nobody can have downloaded yet; otherwise publish a new patch
version.

### First publication

1. Create the public repository `Pukka922/edelweiss` on GitHub. Recommended before the first tag: a tag ruleset
   (Settings → Rules → Rulesets, target: tags) that blocks deleting and updating the tags `v*` and `sdk/v*`, so that a
   published version cannot be moved by mistake (see Releasing above). Then push `main` and the first tag:

   ```sh
   git remote add origin https://github.com/Pukka922/edelweiss.git
   git push -u origin main
   # wait for the CI run on main (or run make lint test sdk-pack-check locally before the push), then:
   git tag v0.1.0
   git push origin v0.1.0
   ```

   The release workflow creates the release `v0.1.0` with the files listed above, `edelweiss-sdk-0.1.0.tgz` among
   them, and sets the tag `sdk/v0.1.0`.
2. Switch the system extension in `../edelweiss-ext-system` to the published SDKs and publish it. Its README,
   [First publication](https://github.com/Pukka922/edelweiss-ext-system#first-publication), has the commands:
   - remove `go.work`, `go.work.sum` and `ui/node_modules`;
   - `go get github.com/Pukka922/edelweiss/sdk@v0.1.0`, then `go mod tidy`, then `npm install` in `ui/`;
   - commit `go.mod`, `go.sum` and `ui/package-lock.json`;
   - create the GitHub repository, then push `main` and the tag `v0.1.0`.
3. Do the same for the example extension in `../edelweiss-ext-hello`. Its README,
   [First publication](https://github.com/Pukka922/edelweiss-ext-hello#first-publication), has the same commands.

Steps 2 and 3 need step 1. Until then, the extension repositories have no `ui/package-lock.json` and no `go.sum`
entry for the SDK, and their workflows fail.

## Layout

```
core/               edelweissd: auth, sessions, extension store and installer, process manager, proxy, CLI, service;
                    core/internal/testext is the test extension of the Go tests and of the mock GitHub
sdk/                Go SDK for extension backends (github.com/Pukka922/edelweiss/sdk)
web/sdk/            JS SDK for extension UIs (@edelweiss/sdk)
web/shell/          the web shell (@edelweiss/shell)
scripts/            npm run watch, the JS SDK tarball (sdk-pack, sdk-pack-check), the Go SDK tag (sdk-tag) and the tests
                    of the tag and of the release guards
dev/                dev container, mock GitHub, smoke and service tests
docs/               documentation
.github/workflows/  ci.yml (lint, tests, SDK tarball check) and release.yml (tag v<version>)
```

## License

MIT, see LICENSE.
