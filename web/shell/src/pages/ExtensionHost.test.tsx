import { createI18n, defineExtension, useExtension, useI18n } from "@edelweiss/sdk";
import { sessionInfo } from "@edelweiss/sdk/testing";
import { createMemoryHistory, MemoryRouter, Route } from "@solidjs/router";
import { cleanup, render } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createShell, ShellProviders } from "../context";
import { fakeFetch, json } from "../test/fake-fetch";
import { ext } from "../test/fixtures";
import { ExtensionHost, loadExtension, type ModuleLoader } from "./ExtensionHost";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  for (const link of document.querySelectorAll("link[data-ext-style]")) link.remove();
});

/** Lets pending fetches, promise callbacks and resource updates settle. */
async function flush() {
  for (let i = 0; i < 5; i++) await new Promise((resolve) => setTimeout(resolve, 0));
}

// Loaded modules are cached per URL for the whole test file, so every test uses its own extension id.
async function mount(
  path: string,
  extensions: ReturnType<typeof ext>[],
  loader: ModuleLoader,
  admin = false,
) {
  const fetch = fakeFetch({
    "GET /api/session": () => json(200, sessionInfo({ admin })),
    "GET /api/extensions": () => json(200, { extensions }),
    "GET /ext/system/ui/locales/en.json": () => json(200, { hello: "Hello from system" }),
    "GET /ext/system/ui/locales/de.json": () => json(200, { hello: "Hallo von system" }),
    "GET /ext/expired/ui/locales/en.json": () =>
      json(401, { error: { code: "unauthenticated", message: "login required" } }),
    "GET /ext/expired/ui/locales/de.json": () =>
      json(401, { error: { code: "unauthenticated", message: "login required" } }),
  });
  vi.stubGlobal("fetch", fetch);
  const shell = createShell({ fetch, locale: "en" });
  await shell.session.load();
  await shell.extensions.reload();
  const history = createMemoryHistory();
  history.set({ value: path });
  const view = render(() => (
    <ShellProviders shell={shell}>
      <MemoryRouter history={history}>
        <Route path="/x/:ext/*rest" component={() => <ExtensionHost loader={loader} />} />
      </MemoryRouter>
    </ShellProviders>
  ));
  return { ...view, shell, fetch };
}

describe("ExtensionHost", () => {
  it("loads the module with its context, locales and styles", async () => {
    const system = {
      ...ext("system", "system", 10),
      ui: {
        entry: "/ext/system/ui/index.js",
        styles: ["/ext/system/ui/style.css"],
        locales: "/ext/system/ui/locales",
      },
    };
    const loader = vi.fn(async () => ({
      default: defineExtension({
        component: () => {
          const { t } = useI18n();
          return (
            <p>
              {useExtension().id}: {t("hello")}
            </p>
          );
        },
      }),
    }));
    const { findByText } = await mount("/x/system", [system], loader);
    expect(await findByText("system: Hello from system")).toBeTruthy();
    expect(loader).toHaveBeenCalledWith("/ext/system/ui/index.js?v=abc");
    expect(document.querySelectorAll('link[data-ext-style="/ext/system/ui/style.css"]')).toHaveLength(1);
  });

  it("adds each stylesheet only once, however often the extension loads", async () => {
    const styled = {
      ...ext("styled", "tools", 1),
      ui: { entry: "/ext/styled/ui/index.js", styles: ["/ext/styled/ui/a.css", "/ext/styled/ui/b.css"] },
    };
    const loader = vi.fn(async () => ({ default: defineExtension({ component: () => null }) }));
    const i18n = createI18n("en");
    const fetchImpl = fakeFetch({});
    await loadExtension(styled, i18n, loader, fetchImpl);
    await loadExtension(styled, i18n, loader, fetchImpl);
    const hrefs = [...document.querySelectorAll<HTMLElement>("link[data-ext-style]")].map(
      (link) => link.dataset.extStyle,
    );
    expect(hrefs).toEqual(["/ext/styled/ui/a.css", "/ext/styled/ui/b.css"]);
    expect(loader).toHaveBeenCalledTimes(1);
  });

  it("reports modules that do not use defineExtension", async () => {
    const other = ext("broken", "tools", 1);
    const { findByRole, fetch } = await mount("/x/broken", [other], async () => ({ default: 42 }));
    expect((await findByRole("alert")).textContent).toContain("broken does not export defineExtension");
    await flush();
    expect(fetch.calls.filter((c) => c.url === "/api/session")).toHaveLength(1); // nothing to do with the session
  });

  it("asks for admin mode for admin-only extensions", async () => {
    const raid = { ...ext("raid", "storage", 1), requiresAdmin: true };
    const loader = vi.fn(async () => ({ default: defineExtension({ component: () => <p>raid</p> }) }));
    const { findByRole, fetch } = await mount("/x/raid", [raid], loader);
    await findByRole("button", { name: "Enable admin mode" });
    // the module is only imported after the locale files were fetched: give it time to (wrongly) happen
    await flush();
    expect(fetch.calls.filter((call) => call.url.startsWith("/ext/raid/"))).toEqual([]);
    expect(loader).not.toHaveBeenCalled();
  });

  it("shows not found for unknown extensions", async () => {
    const { findByRole } = await mount("/x/nope", [], async () => ({}));
    expect((await findByRole("alert")).textContent).toContain("not installed");
  });

  it("shows the missing packages instead of the extension, and loads it once they are installed", async () => {
    const needs = {
      ...ext("needspkg", "containers", 1, "Needs"),
      requires: { apk: ["docker", "cmd:docker-compose"] },
      missingPackages: ["docker", "cmd:docker-compose"],
    };
    const extensions = [needs];
    const loader = vi.fn(async () => ({
      default: defineExtension({ component: () => <p>needs loaded</p> }),
    }));
    const { findByRole, findByText, shell } = await mount("/x/needspkg", extensions, loader);
    const alert = await findByRole("alert");
    expect(alert.textContent).toContain(
      "Needs needs packages that are not installed: docker, cmd:docker-compose.",
    );
    expect(alert.textContent).toContain("apk add docker cmd:docker-compose");
    await flush();
    expect(loader).not.toHaveBeenCalled();

    // apk add as root, then back to the window: the list reloads and the extension loads
    extensions[0] = { ...needs, missingPackages: [] };
    await shell.extensions.reload();
    expect(await findByText("needs loaded")).toBeTruthy();
    expect(loader).toHaveBeenCalledTimes(1);
  });

  it("keeps the extension UI mounted when the extension list reloads unchanged", async () => {
    const stable = ext("stable", "tools", 1);
    let created = 0;
    const loader = vi.fn(async () => ({
      default: defineExtension({
        component: () => {
          created += 1;
          return <p>stable ui</p>;
        },
      }),
    }));
    const { findByText, shell } = await mount("/x/stable", [stable], loader);
    const node = await findByText("stable ui");
    expect(created).toBe(1);

    // same data, new objects: what a refresh on window focus does
    await shell.extensions.reload();
    await flush();
    expect(created).toBe(1);
    expect(node.isConnected).toBe(true);
    expect(loader).toHaveBeenCalledTimes(1);
  });

  it("asks the session when the extension's files answer 401", async () => {
    const expired = {
      ...ext("expired", "tools", 1),
      ui: { entry: "/ext/expired/ui/index.js", styles: [], locales: "/ext/expired/ui/locales" },
    };
    const loader = vi.fn(async () => ({ default: defineExtension({ component: () => <p>never</p> }) }));
    const view = await mount("/x/expired", [expired], loader);
    await flush();
    expect(view.fetch.calls.filter((c) => c.url === "/api/session")).toHaveLength(2); // the initial load and the check
    expect(loader).not.toHaveBeenCalled();
  });

  it("asks the session when the extension's module cannot be loaded", async () => {
    // no locale files here: the failing request is the module itself, which the browser does not let us look at
    const nolocales = {
      ...ext("nolocales", "tools", 1),
      ui: { entry: "/ext/nolocales/ui/index.js", styles: [] },
    };
    const loader = vi.fn(async () => {
      throw new TypeError("Failed to fetch dynamically imported module");
    });
    const view = await mount("/x/nolocales", [nolocales], loader);
    expect((await view.findByRole("alert")).textContent).toContain(
      "Failed to fetch dynamically imported module",
    );
    await flush();
    expect(view.fetch.calls.filter((c) => c.url === "/api/session")).toHaveLength(2); // the initial load and the check
  });

  it("loads the new code when the extension is reinstalled", async () => {
    const original = ext("reinstalled", "tools", 1);
    const extensions = [original];
    let created = 0;
    const loader = vi.fn(async (url: string) => ({
      default: defineExtension({
        component: () => {
          created += 1;
          return <p>{url}</p>;
        },
      }),
    }));
    const { findByText, shell } = await mount("/x/reinstalled", extensions, loader);
    await findByText("/ext/reinstalled/ui/index.js?v=abc");

    extensions[0] = { ...original, source: { ...original.source, sha256: "def" } };
    await shell.extensions.reload();
    expect(await findByText("/ext/reinstalled/ui/index.js?v=def")).toBeTruthy();
    expect(created).toBe(2);
    expect(loader).toHaveBeenCalledTimes(2);
  });
});
