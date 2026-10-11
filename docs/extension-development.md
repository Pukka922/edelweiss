# Developing edelweiss extensions

An edelweiss extension is **one static Linux binary** that contains a Go backend, a SolidJS user interface and a
manifest. edelweissd installs it, starts it when somebody opens it and forwards the requests of the web interface to
it.

An extension lives in its own repository and uses the published SDKs. The example extension
[Pukka922/edelweiss-ext-hello](https://github.com/Pukka922/edelweiss-ext-hello) is the template: copy the repository
and follow its README,
[New extension from this template](https://github.com/Pukka922/edelweiss-ext-hello#new-extension-from-this-template),
which lists what to change. The code snippets below come from it. The system overview,
[Pukka922/edelweiss-ext-system](https://github.com/Pukka922/edelweiss-ext-system), is a larger extension with the
same layout and its backend in `internal/`.

The rules behind that list:

- `BINARY` in the `Makefile` is `edelweiss-ext-<id>`. `make build` and `make release` name the binaries and the
  release files after it (`edelweiss-ext-<id>_<version>_linux_<arch>`), and edelweiss refuses a release file whose name
  does not match the manifest's id and version.
- The `Makefile` assumes the template's layout: `GO_PKGS` is `.`, and `make lint` runs `gofmt` on `*.go`. Add your
  own packages to both, as edelweiss-ext-system does with `./internal/...` and `internal`.
- Keep the `devDependencies` of `ui/package.json`. They include the peer dependencies of the SDK (`solid-js`,
  `@solidjs/router`, `daisyui`, `tailwindcss`, `vite`; the template lists `solid-js` under `dependencies`) and the test
  and build tools of the template (vitest, jsdom, TypeScript, Biome, the Vite plugins).

The SDKs are published with every edelweiss release `v<version>`:

- Go: the module `github.com/Pukka922/edelweiss/sdk`, tagged `sdk/v<version>`. Add it with
  `go get github.com/Pukka922/edelweiss/sdk@v<version>`.
- JavaScript: the tarball `edelweiss-sdk-<version>.tgz`, attached to the edelweiss release; it is not on the npm
  registry.
  - Name its download URL as the dependency in `ui/package.json`:
    `"@edelweiss/sdk": "https://github.com/Pukka922/edelweiss/releases/download/v<version>/edelweiss-sdk-<version>.tgz"`.
    `npm install` records it with its checksum in `ui/package-lock.json`.
  - The tarball contains JavaScript and type definitions for `@edelweiss/sdk`, `@edelweiss/sdk/testing` and
    `@edelweiss/sdk/vite`, and the style sheet `@edelweiss/sdk/extension.css`.

## How an extension runs

- edelweissd reads the manifest by running `<binary> --edelweiss-manifest` (as user `nobody`, with a time limit of
  5 seconds) before it installs the binary. `ext.Run()` of the SDK answers this flag.
- An extension process is started per **user** and **mode**: with the rights of the signed-in user, or as **root**
  while that user is in admin mode. It listens on a Unix socket edelweissd passes in (file descriptor 3) and is
  stopped after `extensions.backend_idle_timeout` without requests, or when the user's last session ends. What it
  writes to stdout and stderr ends up in edelweissd's log.
- The web interface calls `/ext/<id>/api/...`; edelweissd checks the session (and, for requests that change
  something, the CSRF token) and forwards the request with the identity in headers: `X-Edelweiss-User`,
  `X-Edelweiss-Uid`, `X-Edelweiss-Admin` (`0`/`1`), `X-Edelweiss-Lang` (`de`/`en`). Cookies, passwords and CSRF tokens
  never reach the extension.
- `/ext/<id>/ui/...` serves the UI files embedded in the binary.
- Routes below `/_edelweiss` belong to the SDK (status, UI files); edelweissd answers 404 for them on `/ext/<id>/api`.

## Files of an extension

```
edelweiss-ext-<id>/            the layout of edelweiss-ext-hello
  manifest.json                id, version, names, menu entry, UI files
  main.go                      sdk.New + routes + Run, embeds manifest.json and ui/dist
  main_test.go                 checks that the embedded manifest is valid
  greeting.go                  the routes and their handlers
  greeting_test.go             handler tests with sdktest
  go.mod, go.sum               module github.com/<you>/edelweiss-ext-<id>; require github.com/Pukka922/edelweiss/sdk
  Makefile                     build, test, lint, release, release-check, gh-release, dev-sdk
  scripts/                     release-guards-test.sh: the test of release-check and gh-release (make test)
  README.md, LICENSE           what the extension does and how to build and publish it; MIT
  .gitignore                   build/, dist/, go.work, go.work.sum
  .gitattributes               LF line endings, also in a Windows checkout
  .editorconfig                LF, UTF-8, indentation
  .github/workflows/           ci.yml (make lint test build), release.yml (tag v<version>)
  ui/                          a standalone npm project (no workspaces)
    package.json               @edelweiss/sdk as the tarball URL; scripts build, watch, test, typecheck
    package-lock.json          committed once the SDK version that package.json names is published
    vite.config.ts             edelweissExtension()
    vitest.config.ts, tsconfig.json, tsconfig.base.json, biome.json, .gitignore
    src/index.tsx              defineExtension({ component })
    src/Hello.tsx              the page
    src/Hello.test.tsx         its test with TestShell
    src/style.css              @import "@edelweiss/sdk/extension.css"; @source "./";
    public/icon.svg            menu icon
    public/locales/            de.json, en.json
    dist/                      build output (with a committed .gitkeep), embedded by main.go
```

edelweiss-ext-system has the same parts; its backend lives in packages below `internal/`, each with its tests.

## Manifest

```json
{
  "id": "hello",
  "version": "0.1.0",
  "apiVersion": 1,
  "name": { "de": "Hallo", "en": "Hello" },
  "description": { "de": "Beispiel-Extension für Entwickler", "en": "Example extension for developers" },
  "menu": { "section": "tools", "order": 90, "icon": "icon.svg" },
  "ui": { "entry": "index.js", "styles": ["style.css"], "locales": "locales" },
  "requiresAdmin": false
}
```

| Field | Rule |
|---|---|
| `id` | `^[a-z0-9][a-z0-9-]{1,31}$`; `shell` and `sdk` are reserved |
| `version` | semantic version (`1.2.3`, optionally `-pre` or `+build`); must match the release file name |
| `apiVersion` | `1` (the contract: Solid 1.x runtime, SDK, headers) |
| `name` | `en` is required, `de` recommended |
| `menu.section` | `system`, `storage`, `containers`, `network`, `tools` or `other`; `order` sorts within the section |
| `menu.icon` | SVG file in the UI, used as a mask: its shape is drawn in the text colour of the menu |
| `ui` | entry module, style sheets and locale directory, relative to `ui/dist`; `edelweissExtension()` builds `index.js` and `style.css` and refuses other values |
| `requiresAdmin` | `true`: only reachable in admin mode (the extension then always runs as root) |
| `requires.apk` | optional: Alpine packages the extension needs (see below) |

### Required Alpine packages

An extension that needs programs or services from Alpine packages names them in `requires.apk`:

```json
"requires": { "apk": ["docker"] }
```

edelweissd installs or updates the extension only if every listed name is installed: a package name (`docker`) or a
name a package provides (`cmd:docker`, `so:libcrypto.so.3`; `apk info --provides <package>` lists them). Otherwise the
installation stops before anything changes and names the missing packages with the command that installs them
(`apk add docker`, to be run as root); edelweiss never installs packages itself. Version constraints (`docker>=27`)
are not supported, and the list may have at most 32 entries. Each entry consists of letters, digits and `+ . _ : -`,
starts with a letter or digit, has at most 128 characters and appears only once; the manifest is invalid otherwise.
If a package goes missing later (`apk del`), the extension management shows a warning and the extension's page shows
the command instead of the extension.

The extension's backend stays reachable in that case, and a page that is already open keeps running until the
extension list is reloaded. Let the extension's API answer sensibly without its package, for example by reporting
that its service is unavailable, instead of failing with an unspecific error.

The check reads apk's database (`/lib/apk/db/installed`) after the manifest query, so the confirmation dialog of an
installation from GitHub cannot name the packages beforehand.

## Backend (Go)

The snippets leave out the `package` clause and the imports.

`main.go`:

```go
//go:embed manifest.json
var manifest []byte

// The built UI (make build runs npm run build in ui/ first). ui/dist holds a committed .gitkeep, so the package
// compiles before the first UI build too.
//
//go:embed all:ui/dist
var uiFiles embed.FS

func main() {
	ui, err := fs.Sub(uiFiles, "ui/dist")
	if err != nil {
		panic(err)
	}
	ext := sdk.New(sdk.Options{Manifest: manifest, UI: ui})
	register(ext.App)
	ext.Run()
}
```

`greeting.go`:

```go
// Greeting is the answer of GET /greeting.
type Greeting struct {
	Message string `json:"message"`
	User    string `json:"user"`
	Admin   bool   `json:"admin"`
}

var greetings = map[string]string{"de": "Hallo, %s!", "en": "Hello, %s!"}

// register adds the extension's routes. edelweissd forwards /ext/hello/api/* here with the user's identity in
// headers that sdk.UserFrom reads; the extension never sees passwords, cookies or CSRF tokens.
func register(app *fiber.App) {
	app.Get("/greeting", getGreeting)
}

func getGreeting(c fiber.Ctx) error {
	u := sdk.UserFrom(c)
	format, ok := greetings[u.Lang]
	if !ok {
		format = greetings["en"]
	}
	return c.JSON(Greeting{Message: fmt.Sprintf(format, u.Name), User: u.Name, Admin: u.Admin})
}
```

`app` is a Fiber v3 app (`ext.App`); route paths are relative to `/ext/<id>/api`. Build the UI before the Go
binary: `go:embed` takes the files of `ui/dist` at compile time.

The SDK (`github.com/Pukka922/edelweiss/sdk`):

| | |
|---|---|
| `sdk.UserFrom(c)` | `User{Name, UID, Admin, Lang}` of the request (`Lang` is `en` if unknown, `UID` is -1) |
| `sdk.RequireAdmin()` | middleware: `403 admin_required` outside admin mode |
| `sdk.NewError(status, code, message)` | an error answered as `{"error":{"code","message"}}`; `.WithDetails(map[string]any{…})` adds further fields to the error object (call it on a new error, not on a shared variable: it changes the error it is called on) |
| `sdk.SSE(handler)` | Server-Sent Events; read what you need from `c` before the first event and never touch `c` in the loop |
| `sdk.Busy()` | marks long-running work, so the idle reaper does not stop the process; call the returned function when done |
| `sdktest.Request/Do/DecodeJSON` | handler tests with a given user (`github.com/Pukka922/edelweiss/sdk/sdktest`) |

Rules:

- Answer missing permissions with **403** (`sdk.RequireAdmin()` does). A **401** with the code `unauthenticated` is
  the core's answer for "the session is gone"; never use that code in an extension. If an extension answers with it
  anyway, `useApi()` does not sign the user out: it asks the core for the session, and only the core's own
  `unauthenticated` answer sends the user to the login page. A plain Fiber 401 (`fiber.ErrUnauthorized`) is answered
  by the SDK with the code `unauthorized`, which does not touch the session, but 403 is the right answer for a
  permission problem anyway.
- The process runs as the user or as root: check permissions in the backend (`RequireAdmin`, file permissions), never
  trust input from the browser, and keep root-only operations behind admin mode.
- Build static binaries: `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build` (and `arm64`). Dynamically linked binaries
  are refused at install time.

### Data on the host

Keep files your extension stores on the host in `/var/lib/edelweiss-ext-<id>/`, for example a SQLite database
(`modernc.org/sqlite` is a pure Go driver and works with the static build). edelweiss does not create the directory and
leaves its rights to you – remember that the backend runs as the signed-in user, and as root only in admin mode: a
directory that only root may write can be changed in admin mode only. edelweiss deletes the directory when the
extension is removed (`edelweissd ext remove` or the extensions page); an update keeps it.

## User interface (SolidJS)

The shell provides one shared runtime through an import map: `solid-js`, `solid-js/web`, `solid-js/store`,
`@solidjs/router` and `@edelweiss/sdk`. `edelweissExtension()` (from `@edelweiss/sdk/vite`) builds the UI as a
library into `ui/dist` and keeps these modules external – do not bundle them. It also checks `../manifest.json` at
build time (id, `apiVersion`, `name.en`, `ui`).

`src/index.tsx`:

```tsx
import { defineExtension } from "@edelweiss/sdk";
import { Hello } from "./Hello";
import "./style.css";

export default defineExtension({ component: Hello });
```

`src/Hello.tsx` (the logic, without the markup):

```tsx
import { ErrorAlert, LoadingState, PageHeader, useApi, useI18n, useSession } from "@edelweiss/sdk";
import { createMemo, createResource, Match, Show, Switch } from "solid-js";

interface Greeting {
  message: string;
  user: string;
  admin: boolean;
}

export function Hello() {
  const api = useApi();
  const session = useSession();
  const { t, locale } = useI18n();
  // The backend greets in the shell's language (edelweissd forwards it as X-Edelweiss-Lang) and reports admin mode,
  // so ask again when either changes.
  // session.admin() reads the whole session object, which the shell replaces on every focus refresh;
  // the memo notifies only when the boolean actually changes.
  const admin = createMemo(() => session.admin());
  const [greeting, { refetch }] = createResource(
    () => [locale(), admin()] as const,
    () => api.get<Greeting>("/greeting"),
  );
  // …
}
```

| SDK | |
|---|---|
| `useApi()` | client for `/ext/<id>/api`: `get`, `post`, `put`, `patch`, `del` (CSRF header, JSON, `ApiError` with `status`, `code` and `details` – the further fields of the error object) |
| `useStream(path, { event })` | Server-Sent Events as signals `data()` and `status()` |
| `useSocket(path)` | WebSocket to the backend |
| `useSession()` | `user()`, `admin()`, `canAdmin()`, `requestAdmin()` (opens the shell's admin dialog) |
| `useI18n()` | `t(key, params)` with the extension's locale files, `locale()` |
| `useToast()` | `success`, `error`, `info` messages |
| `ExtensionRouter`, `useExtensionNavigate` | pages below `/x/<id>/...` |
| `PageHeader`, `LoadingState`, `ErrorAlert`, `ConfirmDialog`, `AdminRequired` | components |

Styles: `src/style.css` imports `@edelweiss/sdk/extension.css` and lists its sources (`@source "./"`). Use Tailwind
4 and daisyUI 5 classes; the themes (light/dark) come from the shell.

Texts: `public/locales/en.json` and `de.json` (nested keys are allowed, `{{ param }}` placeholders), copied to
`ui/dist/locales` and registered under the extension's id; a missing key falls back to `en`, then to the key itself.
The backend gets the language as `X-Edelweiss-Lang`; if it returns texts, ask it again when `locale()` changes. If
its answer depends on admin mode, ask again when that changes too, and read it through a memo as `Hello.tsx` does.

The shell's Content-Security-Policy allows scripts, requests and fonts from its own origin only, and images from it
or as `data:` URIs: an extension UI cannot load anything from other sites.

Tests: `@edelweiss/sdk/testing` has `TestShell` (all contexts), `fakeSession` and `FakeEventSource`. Add
`@solidjs/router` to the UI package's devDependencies, otherwise vitest cannot compile the SDK's router module. Vitest
runs without globals here, so call `afterEach(cleanup)` yourself. `Hello.test.tsx` shows a page test: it stubs `fetch`,
registers the locale files in a `createI18n("en")` and renders the page in a
`<TestShell extension="hello" i18n={i18n} session={session.api}>`.

## Development loop

Develop against an edelweiss checkout next to your repository (`../edelweiss`; `EDELWEISS_DIR=<path>` for another
place):

```sh
make dev-sdk            # the SDKs of ../edelweiss instead of the published ones
make build test lint
```

`make dev-sdk` does three things:

- it writes a `go.work` (git-ignored) whose `replace` points the Go SDK to `../edelweiss/sdk`;
- it builds the JS SDK tarball there (`make sdk-pack`);
- it installs the tarball into `ui/node_modules` with `npm install --no-save --no-package-lock`, so `ui/package.json`
  stays unchanged and no `package-lock.json` is written.

None of this needs github.com, so it also works for an SDK version that is not published yet.

- Do not run `go mod tidy`, `go get` for the SDK, or a plain `npm install` while that version is unpublished: they
  look it up on GitHub.
- `rm -f go.work go.work.sum && rm -rf ui/node_modules` switches back to the published SDKs. The next `make` runs
  `npm ci` once a committed `ui/package-lock.json` exists; without one, `make` asks for `make dev-sdk` again.

To try the extension, start the edelweiss dev container (`make dev` in `../edelweiss`) and install your release file
into it (use `linux_arm64` on an arm64 machine). `make dev` builds the system overview too, so it needs a checkout of
edelweiss-ext-system at `../edelweiss-ext-system` (or `EXT_SYSTEM_DIR=<path>` set) and stops with a message if there is
none.

```sh
make release
mkdir -p ../edelweiss/build
cp dist/edelweiss-ext-<id>_<version>_linux_amd64 ../edelweiss/build/
cd ../edelweiss
docker compose -f dev/compose.yaml exec edelweiss /work/build/dev/edelweissd ext install \
  /work/build/edelweiss-ext-<id>_<version>_linux_amd64 --config /work/dev/edelweiss.dev.toml
```

The running edelweissd picks the extension up within a moment. Install again after a change: that replaces the
extension and stops its processes.

The system overview runs in the dev container as a *development extension* instead (`[[dev.extensions]]` in
`dev/edelweiss.dev.toml`). edelweissd restarts it when its binary changes (`make dev-build`) and serves its UI straight
from `../edelweiss-ext-system/ui/dist`, so `npm run watch` in its `ui/` and a page reload are enough for UI changes.
Development extensions carry a `dev` badge on the extensions page and cannot be removed there.

`make dev` also starts a mock GitHub, so the install flow can be tried without GitHub: in the dev container, install
`edelweiss/testext` under **Settings → Extensions → Install from GitHub**. `make mock-releases` builds the releases
it serves from edelweiss's test extension (`core/internal/testext`): version 0.1.0 and the update 0.2.0. `make smoke`
checks the whole container, installer included.

## Publishing a release

The template's release workflow (`.github/workflows/release.yml`) publishes a release when you push a tag
`v<version>`. It has two jobs, so that the build cannot use the write token. `build` has a read-only token and keeps no
credentials in the checkout. It checks the tag name (`v`, then only letters, digits, `.`, `+` and `-`), then runs three
make targets and hands `dist/` on as a workflow artifact:

- `make release-check`: the tag must match the manifest's version, which has to be a plain `x.y.z` or `x.y.z-pre` by
  the rules of Go (no leading zeros, no empty pre-release identifier, no `+build` metadata);
- `make lint test`;
- `make release`: the files below, in `dist/`.

`publish` runs when `build` has succeeded. It holds the write token, builds nothing and needs neither Go nor Node. It
downloads the artifact into `dist/` and runs `make gh-release`: it checks that `dist/` holds exactly `checksums.txt`
and the two binaries of this version, that the files match `checksums.txt` and that it lists exactly them, then runs
`gh release create` with these files. A version with a `-` (`0.2.0-rc.1`) is published as a GitHub pre-release, which
the installer's "latest release" leaves out, so an rc is not offered as an update.

Set the version in `manifest.json`, commit and push `main`, then:

```sh
make release-check VERSION=0.2.0
git tag v0.2.0
git push origin v0.2.0
```

The release files:

```
edelweiss-ext-<id>_<version>_linux_amd64
edelweiss-ext-<id>_<version>_linux_arm64
checksums.txt
```

- Names follow `edelweiss-ext-<id>_<version>_linux_<amd64|arm64>`; `<id>` and `<version>` must equal the manifest's.
- Instead of the raw binary, `<name>.tar.gz` with exactly one executable file (plus README/LICENSE) works too.
- `checksums.txt` lists every file in `sha256sum` format. Any name ending in `checksums.txt` works, so goreleaser's
  `<project>_<version>_checksums.txt` does too. Without an entry for the file, edelweiss refuses to install it.
- A release may contain several extensions; users choose with `--id` or in the dialog.
- Rerunning a failed workflow is fine as long as the tag stays on the same commit ("Re-run failed jobs" repeats only the
  failed job, "Re-run all jobs" builds again). `gh release create` fails if the release already exists, so if the
  workflow had already created it, delete only the release (never the tag) first.
- The CI workflow (`ci.yml`) runs `make lint test build` on every push and pull request.
- Both workflows use the published SDKs (`go.sum`, `ui/package-lock.json`), so they work once the SDK version you
  require is published.

Users install with `edelweissd ext install github.com/<you>/<repo>`. A newer semantic version in the repository's
latest release shows up as an update. Every install and update shows the user the repository, release, file and
SHA-256 and the warning that an extension runs with the user's rights (as root in admin mode), and installs exactly
that file: if the release changes after the user looked at it, the install or update fails with `release_changed`.
Once a release may have been fetched, never move or delete its tag and never replace its files, because edelweiss
refuses the changed release (`release_changed`) and sum.golang.org records every Go module version permanently. Fix a
broken release with a new version.

## Security notes

- An extension runs with the rights of the signed-in user, and as root in admin mode. Its UI runs in the shell's page
  (no sandbox) and its CSS applies to the whole page. Users are told to install only from trusted sources; behave
  accordingly.
- edelweissd requires a session for every `/ext/<id>/api` request, and a CSRF token and a same-origin check for every
  request other than GET, HEAD and OPTIONS (WebSocket connections get the same-origin check). The extension still
  validates every input.
