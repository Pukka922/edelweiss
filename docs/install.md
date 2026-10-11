# Installing edelweiss on Alpine Linux

edelweiss is a web administration interface for Alpine Linux. It is one static binary (`edelweissd`) plus
extensions, which are single static binaries too.

## Requirements

- Alpine Linux 3.24, 64-bit (x86_64 or aarch64), with OpenRC (the default); 32-bit systems are not supported. A
  diskless system (running from RAM) also needs the `lbu` commands in [README.md](../README.md#4-diskless-mode-only).
- A syslog service for the logs (the default busybox `syslogd`: `rc-update add syslog boot` if it is not enabled).
- `ca-certificates-bundle` (installed by default) to install extensions from GitHub.
- root access for the installation; an account in the group `wheel` for administrative work (root itself cannot
  sign in to edelweiss).

## 1. Get the binaries

Download `edelweissd_<version>_linux_<arch>` and `checksums.txt` from the
[latest edelweiss release](https://github.com/Pukka922/edelweiss/releases/latest); `<arch>` is `amd64` on x86_64 and
`arm64` on aarch64. Or build them on a development machine (see [README.md](../README.md#building)):

```sh
make build
ls dist/
# checksums.txt  edelweissd_0.1.0_linux_amd64  edelweissd_0.1.0_linux_arm64
```

Copy the binary and `checksums.txt` to the host, for example with `scp`, and verify it there:
`grep linux_amd64 checksums.txt | sha256sum -c` (`linux_arm64` on aarch64). The release's `checksums.txt` also lists the
JS SDK for extension developers (`edelweiss-sdk-<version>.tgz`). The host does not need it, and the `grep` leaves it
out. The extensions, the system overview among them, are installed in step 4.

## 2. Install the service

```sh
mkdir -p /usr/local/sbin                       # the minimal Alpine image has no /usr/local/sbin
install -m 755 edelweissd_0.1.0_linux_amd64 /usr/local/sbin/edelweissd
edelweissd service install
```

`service install` must run as root. It writes the OpenRC service for the binary at its current path, so install the
binary at its final location first; the path must be absolute and consist of letters, digits and `./_+-`. If OpenRC
is missing (`rc-update` is not found), it stops before it writes anything.

| File | Content |
|---|---|
| `/etc/init.d/edelweiss` | the service (supervise-daemon: restarts edelweissd when it stops unexpectedly, first after 2 seconds, and gives up when it stops more than 5 times within 12 seconds); rewritten on every install |
| `/etc/conf.d/edelweiss` | service settings: `EDELWEISS_CONFIG`, `EDELWEISS_ARGS`, the file descriptor limit; written only if missing |
| `/etc/edelweiss/edelweiss.toml` | the configuration with all defaults (mode 0600); written only if missing |
| `/var/lib/edelweiss/` | state: installed extensions, the TLS certificate (mode 0711) |

An existing configuration and an existing `/etc/conf.d/edelweiss` are never touched, so it is safe to run
`service install` again, for example after you moved the binary. The command also adds the service to the default
runlevel (`rc-update add edelweiss default`); it does not start it.

## 3. Create an administrator

```sh
adduser myUser
addgroup myUser wheel
```

A Linux account can sign in when it has a password that is not locked (the hash in `/etc/shadow` does not start with
`!` or `*`), is not expired, uses a supported hash (md5-crypt, sha256-crypt, sha512-crypt, bcrypt or yescrypt) and
is not listed in `auth.deny_users` (by default `root`). The login shell is not checked. An account with an unsupported
hash cannot sign in either; the log says why.

Members of `wheel` (`auth.admin_group`) can switch to *admin mode* after entering their password again; in admin
mode extensions run with root rights.

## 4. Install extensions

Extensions are installed in the web interface, under **Settings → Extensions → Install from GitHub** (admin mode),
for example the system overview (host name, operating system, live CPU, memory, disk and network usage) from
[Pukka922/edelweiss-ext-system](https://github.com/Pukka922/edelweiss-ext-system). The details are below under "From a
GitHub release".

The same works on the command line. Run the `ext` commands as root, on the host. All of them accept `--config <file>`
(default `/etc/edelweiss/edelweiss.toml`).

From a file (a host without access to GitHub can install a release file this way; check it with its `checksums.txt`
first):

```sh
edelweissd ext install ./edelweiss-ext-<id>_<version>_linux_amd64
```

An argument that is an existing file is installed as a file; start a relative path with `./`. A file is installed
without a question. `--id <id>` makes the install fail unless the extension in the file has that id.

From a GitHub release (the latest one, or a tag):

```sh
edelweissd ext install github.com/owner/repo
edelweissd ext install owner/repo@v1.2.0
```

`https://github.com/owner/repo` and `https://github.com/owner/repo/releases/tag/v1.2.0` work as well. edelweiss
looks the release up and shows what it would install, together with a warning, before it installs anything:

```
Repository: owner/repo
Release:    v1.2.0 (published 2026-10-01)
Extension:  hello 1.2.0
File:       edelweiss-ext-hello_1.2.0_linux_amd64 (8123456 bytes)
SHA-256:    <the checksum the release lists for the file>
Warning: an extension runs with the privileges of the signed-in user, in admin mode as root.
Only install extensions from sources you trust.
Install hello 1.2.0? [y/N]
```

Only `y` or `yes` installs. `--yes` skips the question (for scripts); the information and the warning are still
printed. `--id <id>` chooses an extension when a release contains several. edelweiss only installs files that are
listed with a matching checksum in the release's `checksums.txt`.

What you confirm is what is installed: the command installs exactly the release and the file it showed, even if a
newer release appears meanwhile. If the release was changed after you looked at it, the installation fails with
"the release changed since it was reviewed" (`release_changed`, HTTP status 409 in the API) and nothing is installed.

In the web interface the same works under **Settings → Extensions → Install from GitHub** (admin mode): enter
`owner/repo` (optionally `@tag`), choose **Find release**, check the repository, release, file and SHA-256 and the
warning, then choose **Install**.

Updates:

```sh
edelweissd ext update --all     # or: edelweissd ext update <id>
```

This looks for a newer latest release of every extension that was installed from GitHub (or of the one you name),
lists the repository, release, file and SHA-256 of each update with the same warning, and asks for confirmation
(`--yes` skips the question). Extensions installed from a file cannot be updated this way. Like an install, an update
installs exactly the file you confirmed, and fails if the release changed since.

The extensions page of the web interface checks for updates when you open it in admin mode or turn admin mode on
there, after an install from GitHub, and again with **Check for updates**. It offers each one as an **Update to**
button with the new version; the confirmation dialog shows the same details and the same warning as the command
line.

Other commands: `edelweissd ext list`, `edelweissd ext remove <id>` (the extensions page can remove extensions
too). A running service picks up every change made with the command line within a moment; it stops the processes of
a changed extension, and they start again with the next request.

The service runs one install or update at a time; a second one from the web interface is refused with "another
installation is running". That only holds inside the service: the `edelweissd ext` commands work on the extension store
directly and are not coordinated with the service or with each other, so do not run one while an install or update
is in progress elsewhere.

### Extensions that need Alpine packages

Some extensions need Alpine packages; a Docker management extension, for example, needs `docker`. If one is missing,
the installation stops before anything changes and names it:

```
install failed: missing packages: docker
install them as root first: apk add docker
```

Install the packages as root (`apk add docker`) and run the installation again; the web UI names the same packages and the same command. If
a package is removed later, the extension's page shows the command instead of the extension until the package is back.

### Data of extensions

Extensions keep their data in `/var/lib/edelweiss-ext-<id>/` (the Docker management, for example, its registry list).
Removing an extension deletes that directory; an update keeps it. Back it up together with `/var/lib/edelweiss`; in
diskless mode `lbu include /var/lib/edelweiss-ext-<id>` as well.

### Docker management

[Pukka922/edelweiss-ext-docker](https://github.com/Pukka922/edelweiss-ext-docker) manages the containers, images,
volumes, networks and registries of the local Docker daemon. It needs Docker on the host. As root:

```sh
apk add docker
rc-update add docker default
rc-service docker start
addgroup myUser docker                          # optional: Docker without admin mode for this account
edelweissd ext install Pukka922/edelweiss-ext-docker
```

Members of the group `docker` use Docker without admin mode; the membership takes effect at their next sign-in to
edelweiss. Everybody else needs admin mode. Membership of `docker` is as powerful as root: a container can mount the
host's file system. The extension's README describes the rest (registries, registries without TLS, memory figures on a
Raspberry Pi).

## 5. Start

```sh
rc-service edelweiss start
```

Open `https://<host>:9090`. edelweiss creates a self-signed certificate on the first start; compare the fingerprint
your browser shows with the one in the log, which is written once, when the certificate is created:

```sh
grep 'self-signed' /var/log/messages
```

Later, `openssl x509 -in /var/lib/edelweiss/tls/cert.pem -noout -fingerprint -sha256` prints it (`apk add openssl`).

To use your own certificate, set `tls.cert` and `tls.key` in the configuration.

### Firewall

Allow TCP port 9090 (the port of `listen`). With nftables, for example, add to the input chain:

```
tcp dport 9090 accept
```

## Configuration

`/etc/edelweiss/edelweiss.toml`; restart the service after a change (`rc-service edelweiss restart`). A key that
does not exist is an error: edelweissd refuses to start.

| Key | Default | Meaning |
|---|---|---|
| `listen` | `"0.0.0.0:9090"` | IPv4 address and port |
| `tls.enabled` | `true` | HTTPS; plain HTTP is meant for development only |
| `tls.cert`, `tls.key` | `""` | certificate and key files, set together; empty: a self-signed certificate in `<state_dir>/tls`, valid for a year and renewed before it expires (a restart loads the renewed one) |
| `auth.admin_group` | `"wheel"` | group whose members may use admin mode |
| `auth.deny_users` | `["root"]` | accounts that may not sign in |
| `auth.session_idle_timeout` | `"15m"` | sign-out after this time without activity |
| `auth.session_max_age` | `"12h"` | sign-out after this time at the latest |
| `auth.shadow_file` | `"/etc/shadow"` | password database |
| `extensions.backend_idle_timeout` | `"10m"` | an idle extension process is stopped after this time |
| `extensions.max_download_size` | `"200MB"` | largest extension download from GitHub |
| `github.api_url` | `"https://api.github.com"` | GitHub API (GitHub Enterprise: its API URL) |
| `github.token` | `""` | optional token: higher API rate limit, private repositories; sent only to the API host |
| `log.level` | `"info"` | `debug`, `info`, `warn` or `error` |
| `paths.state_dir` | `"/var/lib/edelweiss"` | installed extensions, temporary downloads, TLS certificate |
| `paths.run_dir` | `"/run/edelweiss"` | sockets of the extension processes, lock file |

Durations use the syntax `90s`, `15m`, `12h` (there is no `d`). Sizes are `200MB`, `1GB`, `512KB` or a number of bytes
(1 MB = 1024 KB). Paths must be absolute.

## Operation

```sh
rc-service edelweiss status
rc-service edelweiss restart
grep edelweiss /var/log/messages     # the log (also: login, admin mode, installs, removals)
```

**Upgrading edelweissd:** replace `/usr/local/sbin/edelweissd`, run `edelweissd service install` again and then
`rc-service edelweiss restart`. Moving the new file over the old one works while the old one runs:

```sh
install -m 755 edelweissd_0.2.0_linux_amd64 /usr/local/sbin/edelweissd.new
mv /usr/local/sbin/edelweissd.new /usr/local/sbin/edelweissd
edelweissd service install     # rewrites the init script; the configuration and conf.d file are kept
rc-service edelweiss restart
```

The init script is rewritten on every `service install`, so a new release can change it (the new binary writes its own
version); without this step the service keeps the old script.

A restart stops the extension processes and signs everybody out (sessions are kept in memory only).

## Uninstalling

```sh
edelweissd service uninstall           # stops the service, removes it from the runlevel, removes the init script
edelweissd service uninstall --purge   # also removes /etc/conf.d/edelweiss, /etc/edelweiss and /var/lib/edelweiss (extensions!)
rm /usr/local/sbin/edelweissd
```

Without `--purge` the settings (`/etc/conf.d/edelweiss` and `/etc/edelweiss/edelweiss.toml`) and the state in
`/var/lib/edelweiss` stay, so a later `service install` picks them up again. Nothing is deleted if the service cannot
be stopped.

`--purge` leaves the data directories of extensions (`/var/lib/edelweiss-ext-*`): remove the extensions first (`edelweissd ext remove <id>`), or delete the directories yourself.

## Troubleshooting

| Message | Cause and fix |
|---|---|
| `binary is dynamically linked or its interpreter is missing` | the extension was built against glibc; extensions must be built with `CGO_ENABLED=0` |
| `binary cannot be executed on this system` | wrong architecture (an amd64 binary on aarch64 or the other way round) |
| `the release contains no edelweiss extension for this system` | the release has no `edelweiss-ext-<id>_<version>_linux_<arch>` file for this architecture |
| `the release has no checksums.txt that lists the extension` | the publisher must attach a `checksums.txt` (sha256sum format) that lists the file |
| `the download does not match its checksum` | the file is corrupt or was changed; nothing was installed |
| `the release changed since it was reviewed` | the release was modified after you looked at it; look it up again and review it |
| `download exceeds the size limit` | the file is larger than `extensions.max_download_size` |
| `GitHub API rate limit exceeded` | wait, or set `github.token` |
| `the release contains several extensions; choose one by id (…)` | the release offers more than one extension for this system; name one with `--id <id>` (the message lists the ids), or choose it in the web interface |
| `another installation is running` | an install or update from the web interface is still in progress; try again in a moment |
| `edelweissd is already running` | a second instance on the same `run_dir`; stop the other one |
| `OpenRC's rc-update was not found` | `service install` needs Alpine with OpenRC; nothing was written |
| root cannot sign in | intended (`auth.deny_users`); sign in as a member of `wheel` and use admin mode |
