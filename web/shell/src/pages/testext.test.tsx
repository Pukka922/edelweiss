import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { sessionInfo } from "@edelweiss/sdk/testing";
import { createMemoryHistory, MemoryRouter, Route } from "@solidjs/router";
import { cleanup, render } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import { sharedSpecifiers } from "../../plugins/vendor.ts";
import { createShell, ShellProviders } from "../context";
import { fakeFetch, json } from "../test/fake-fetch";
import { ext } from "../test/fixtures";
import { ExtensionHost } from "./ExtensionHost";

// The page of core/internal/testext: plain JavaScript that the core serves as it is, without a build step.
const page = resolve(import.meta.dirname, "../../../../core/internal/testext/ui/index.js");

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

async function mount(versionAnswer: () => Response) {
  const fetch = fakeFetch({
    "GET /api/session": () => json(200, sessionInfo()),
    "GET /api/extensions": () =>
      json(200, {
        extensions: [
          { ...ext("testext", "tools", 99), ui: { entry: "/ext/testext/ui/index.js", styles: [] } },
        ],
      }),
    "GET /ext/testext/api/version": versionAnswer,
  });
  vi.stubGlobal("fetch", fetch);
  const shell = createShell({ fetch, locale: "en" });
  await shell.session.load();
  await shell.extensions.reload();
  const history = createMemoryHistory();
  history.set({ value: "/x/testext" });
  const loader = vi.fn((_url: string) => import(/* @vite-ignore */ page));
  const view = render(() => (
    <ShellProviders shell={shell}>
      <MemoryRouter history={history}>
        <Route path="/x/:ext/*rest" component={() => <ExtensionHost loader={loader} />} />
      </MemoryRouter>
    </ShellProviders>
  ));
  return { ...view, loader };
}

describe("the testext page", () => {
  it("imports only modules of the shell's import map", () => {
    // Without its comment lines: the header says "import map", which is no import.
    const source = readFileSync(page, "utf8").replace(/^[ \t]*\/\/.*$/gm, "");
    const specifiers = [...source.matchAll(/^import .* from "([^"]+)";$/gm)].map((m) => m[1]);
    expect(specifiers).toEqual(["@edelweiss/sdk", "solid-js"]);
    for (const s of specifiers) expect(sharedSpecifiers).toContain(s);
    // The pattern above sees only single-line static imports. Every `import` of the page has to be one of them: a
    // side-effect, multi-line or dynamic import would be one more occurrence and fails here.
    expect(source.match(/\bimport\b/g) ?? []).toHaveLength(specifiers.length);
  });

  it("is an extension module that shows the version of the running binary", async () => {
    const view = await mount(() => json(200, { version: "0.2.0" }));
    expect(await view.findByRole("heading", { name: "Test extension" })).toBeTruthy();
    expect(await view.findByText("Version 0.2.0")).toBeTruthy();
    expect(view.loader).toHaveBeenCalledWith("/ext/testext/ui/index.js?v=abc");
  });

  it("shows the error when the backend does not answer", async () => {
    const view = await mount(() =>
      json(500, { error: { code: "internal_error", message: "testext is down" } }),
    );
    expect(await view.findByText("testext is down")).toBeTruthy();
  });
});
