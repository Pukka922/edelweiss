import { ExtensionContext, extensionContextFor, useApi, useI18n, useSession, useToast } from "@edelweiss/sdk";
import { sessionInfo } from "@edelweiss/sdk/testing";
import { cleanup, render } from "@solidjs/testing-library";
import { createEffect, createRoot, on } from "solid-js";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createShell, ShellProviders, useShell } from "./context";
import { fakeFetch, json } from "./test/fake-fetch";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  localStorage.clear();
  // biome-ignore lint/suspicious/noDocumentCookie: removes the cookie applyLocale() wrote
  document.cookie = "edelweiss_lang=; Path=/; Max-Age=0";
  document.documentElement.removeAttribute("lang");
});

describe("createShell", () => {
  it("applies the initial locale for extension backends right away, without storing it", () => {
    vi.spyOn(navigator, "language", "get").mockReturnValue("de-DE");
    expect(document.cookie).not.toContain("edelweiss_lang");
    const shell = createShell({ fetch: fakeFetch({}) });
    expect(shell.i18n.locale()).toBe("de");
    expect(document.cookie).toContain("edelweiss_lang=de");
    expect(document.documentElement.lang).toBe("de");
    expect(localStorage.getItem("edelweiss.lang")).toBeNull();
  });

  it("stores the locale once the user changes it", () => {
    const shell = createShell({ locale: "de", fetch: fakeFetch({}) });
    expect(localStorage.getItem("edelweiss.lang")).toBeNull();
    shell.setLocale("en");
    expect(shell.i18n.locale()).toBe("en");
    expect(document.cookie).toContain("edelweiss_lang=en");
    expect(document.documentElement.lang).toBe("en");
    expect(localStorage.getItem("edelweiss.lang")).toBe("en");
  });

  it("writes the language cookie before the locale signal changes", () => {
    const shell = createShell({ locale: "en" });
    const seen: string[] = [];
    const dispose = createRoot((dispose) => {
      createEffect(on(shell.i18n.locale, () => seen.push(document.cookie), { defer: true }));
      return dispose;
    });
    shell.setLocale("de");
    dispose();
    expect(seen).toHaveLength(1);
    expect(seen[0]).toContain("edelweiss_lang=de");
    expect(document.documentElement.lang).toBe("de");
  });
});

describe("ShellProviders", () => {
  it("provides the shell and the SDK contexts extensions rely on", async () => {
    const shell = createShell({
      locale: "en",
      theme: "light",
      fetch: fakeFetch({ "POST /api/auth/login": () => json(200, sessionInfo({ csrfToken: "c-5" })) }),
    });
    await shell.session.login("alice", "pw");
    const extFetch = vi.fn(async () => json(200, { ok: true }));
    vi.stubGlobal("fetch", extFetch);

    let seen: ReturnType<typeof collect> | undefined;
    const collect = () => ({
      shell: useShell(),
      session: useSession(),
      toasts: useToast(),
      i18n: useI18n("shell"),
      api: useApi(),
    });
    render(() => (
      <ShellProviders shell={shell}>
        <ExtensionContext.Provider value={extensionContextFor("system")}>
          {(() => {
            seen = collect();
            return null;
          })()}
        </ExtensionContext.Provider>
      </ShellProviders>
    ));

    expect(seen!.shell).toBe(shell);
    expect(seen!.session).toBe(shell.session);
    expect(seen!.toasts).toBe(shell.toasts);
    expect(seen!.i18n.t("nav.settings")).toBe("Settings");
    shell.setLocale("de");
    expect(seen!.i18n.t("nav.settings")).toBe("Einstellungen");

    // useApi() hands session.csrfToken to the API client unbound
    await seen!.api.post("/restart");
    const [url, init] = extFetch.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("/ext/system/api/restart");
    expect((init.headers as Record<string, string>)["X-Csrf-Token"]).toBe("c-5");
  });

  describe("when an extension backend answers 401 unauthenticated", () => {
    // The shell asks the core (GET /api/session); only the core's answer decides whether the session is over.
    async function extensionApi(sessionAnswer: () => Response) {
      const shell = createShell({
        locale: "en",
        fetch: fakeFetch({
          "POST /api/auth/login": () => json(200, sessionInfo()),
          "GET /api/session": sessionAnswer,
        }),
      });
      await shell.session.login("alice", "pw");
      vi.stubGlobal("fetch", async () =>
        json(401, { error: { code: "unauthenticated", message: "login required" } }),
      );
      let api: ReturnType<typeof useApi> | undefined;
      render(() => (
        <ShellProviders shell={shell}>
          <ExtensionContext.Provider value={extensionContextFor("system")}>
            {(() => {
              api = useApi();
              return null;
            })()}
          </ExtensionContext.Provider>
        </ShellProviders>
      ));
      return { shell, api: api! };
    }

    it("keeps the session while the core still knows it", async () => {
      const { shell, api } = await extensionApi(() => json(200, sessionInfo({ hostname: "refreshed" })));
      await expect(api.get("/info")).rejects.toMatchObject({ status: 401 });
      await vi.waitFor(() => expect(shell.session.info()?.hostname).toBe("refreshed"));
      expect(shell.session.state()).toBe("authenticated");
      expect(shell.session.expired()).toBe(false);
    });

    it("ends the session when the core says it is gone", async () => {
      const { shell, api } = await extensionApi(() =>
        json(401, { error: { code: "unauthenticated", message: "login required" } }),
      );
      await expect(api.get("/info")).rejects.toMatchObject({ status: 401 });
      await vi.waitFor(() => expect(shell.session.state()).toBe("anonymous"));
      expect(shell.session.expired()).toBe(true);
    });
  });
});
