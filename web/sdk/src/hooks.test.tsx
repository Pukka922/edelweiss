import { render } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ExtensionContext, extensionContextFor } from "./extension";
import { useApi } from "./hooks";
import { type SessionApi, SessionContext } from "./session";
import { fakeSession } from "./test-utils";

afterEach(() => vi.unstubAllGlobals());

function renderApi(session: SessionApi) {
  let api: ReturnType<typeof useApi> | undefined;
  render(() => (
    <SessionContext.Provider value={session}>
      <ExtensionContext.Provider value={extensionContextFor("system")}>
        {(() => {
          api = useApi();
          return null;
        })()}
      </ExtensionContext.Provider>
    </SessionContext.Provider>
  ));
  return api!;
}

function answer401(code: string) {
  vi.stubGlobal(
    "fetch",
    async () => new Response(JSON.stringify({ error: { code, message: "login required" } }), { status: 401 }),
  );
}

describe("useApi", () => {
  it("talks to the extension's API base with the session's CSRF token", async () => {
    const fetch = vi.fn(async () => new Response(JSON.stringify({ ok: 1 }), { status: 200 }));
    vi.stubGlobal("fetch", fetch);
    const session = fakeSession({ csrfToken: "tok-7" });
    const api = renderApi(session.api);
    await api.put("/hostname", { hostname: "x" });
    const [url, init] = fetch.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("/ext/system/api/hostname");
    expect((init.headers as Record<string, string>)["X-Csrf-Token"]).toBe("tok-7");
  });

  it("asks the core when an extension answers 401 unauthenticated: the session is refreshed, not ended", async () => {
    answer401("unauthenticated");
    const session = fakeSession();
    const api = renderApi(session.api);
    await expect(api.get("/info")).rejects.toMatchObject({ status: 401 });
    expect(session.calls.refresh).toBe(1);
    expect(session.calls.unauthorized).toBe(0);
  });

  it("ignores a failing session refresh after an extension 401", async () => {
    answer401("unauthenticated");
    const session = fakeSession();
    const refresh = vi.fn(async () => {
      throw new Error("offline");
    });
    const api = renderApi({ ...session.api, refresh });
    await expect(api.get("/info")).rejects.toMatchObject({ status: 401 });
    await Promise.resolve();
    expect(refresh).toHaveBeenCalledTimes(1);
    expect(session.calls.unauthorized).toBe(0);
  });

  it("leaves the session alone for an extension's own 401", async () => {
    answer401("unauthorized");
    const session = fakeSession();
    const api = renderApi(session.api);
    await expect(api.get("/info")).rejects.toMatchObject({ status: 401, code: "unauthorized" });
    expect(session.calls.refresh).toBe(0);
    expect(session.calls.unauthorized).toBe(0);
  });
});
