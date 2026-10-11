import { sessionInfo } from "@edelweiss/sdk/testing";
import { createEffect, createRoot } from "solid-js";
import { describe, expect, it, vi } from "vitest";
import { type FakeRequest, fakeFetch, json } from "../test/fake-fetch";
import { createSessionStore } from "./session";

/** A GET /api/session whose answers the test gives by hand: held[n] answers the n-th request. */
function heldSessionAnswers() {
  const held: ((answer: Response) => void)[] = [];
  const route = () =>
    new Promise<Response>((resolve) => {
      held.push(resolve);
    });
  return { held, route };
}

const sessionGone = () => json(401, { error: { code: "unauthenticated", message: "login required" } });
const settle = () => new Promise((resolve) => setTimeout(resolve, 0));

describe("session store", () => {
  it("loads an existing session or ends up anonymous", async () => {
    const ok = createSessionStore(fakeFetch({ "GET /api/session": () => json(200, sessionInfo()) }));
    expect(ok.state()).toBe("loading");
    await ok.load();
    expect(ok.state()).toBe("authenticated");
    expect(ok.user()?.name).toBe("alice");

    const anon = createSessionStore(
      fakeFetch({ "GET /api/session": () => json(401, { error: { code: "unauthenticated", message: "" } }) }),
    );
    await anon.load();
    expect(anon.state()).toBe("anonymous");
    expect(anon.expired()).toBe(false);
  });

  it("logs in with JSON and marks a later 401 as expired session", async () => {
    const fetch = fakeFetch({
      "POST /api/auth/login": () => json(200, sessionInfo()),
      "GET /api/extensions": () => json(401, { error: { code: "unauthenticated", message: "" } }),
    });
    const s = createSessionStore(fetch);
    await s.login("alice", "pw");
    expect(fetch.calls[0]).toMatchObject({ body: { username: "alice", password: "pw" } });
    expect(fetch.calls[0]!.headers["Content-Type"]).toBe("application/json");
    expect(s.state()).toBe("authenticated");
    await s.api.get("/extensions").catch(() => {});
    expect(s.state()).toBe("anonymous");
    expect(s.expired()).toBe(true);
  });

  it("keeps the session when the admin password is wrong", async () => {
    const s = createSessionStore(
      fakeFetch({
        "POST /api/auth/login": () => json(200, sessionInfo()),
        "POST /api/session/admin": () => json(401, { error: { code: "invalid_credentials", message: "x" } }),
      }),
    );
    await s.login("alice", "pw");
    await expect(s.enableAdmin("wrong")).rejects.toMatchObject({ code: "invalid_credentials" });
    expect(s.state()).toBe("authenticated");
  });

  it("requestAdmin waits for the admin dialog", async () => {
    const s = createSessionStore(
      fakeFetch({
        "POST /api/auth/login": () => json(200, sessionInfo({ canAdmin: true })),
        "POST /api/session/admin": () => json(200, sessionInfo({ admin: true, csrfToken: "csrf-2" })),
      }),
    );
    await s.login("alice", "pw");
    const pending = s.requestAdmin();
    expect(s.adminRequested()).toBe(true);
    await s.enableAdmin("pw");
    expect(await pending).toBe(true);
    expect(s.adminRequested()).toBe(false);
    expect(s.admin()).toBe(true);
    expect(s.csrfToken()).toBe("csrf-2");
    expect(await s.requestAdmin()).toBe(true);
  });

  it("requestAdmin resolves false when cancelled or not permitted", async () => {
    const s = createSessionStore(
      fakeFetch({ "POST /api/auth/login": () => json(200, sessionInfo({ canAdmin: true })) }),
    );
    await s.login("alice", "pw");
    const pending = s.requestAdmin();
    s.answerAdminRequest(false);
    expect(await pending).toBe(false);

    const plain = createSessionStore(
      fakeFetch({ "POST /api/auth/login": () => json(200, sessionInfo({ canAdmin: false })) }),
    );
    await plain.login("bob", "pw");
    expect(await plain.requestAdmin()).toBe(false);
  });

  it("logs out with the CSRF token", async () => {
    const fetch = fakeFetch({
      "POST /api/auth/login": () => json(200, sessionInfo({ csrfToken: "c-9" })),
      "POST /api/auth/logout": () => new Response(null, { status: 204 }),
    });
    const s = createSessionStore(fetch);
    await s.login("alice", "pw");
    await s.logout();
    expect(fetch.calls[1]!.headers["X-Csrf-Token"]).toBe("c-9");
    expect(s.state()).toBe("anonymous");
  });

  it("works with unbound functions, as the SDK's useApi() passes them around", async () => {
    const s = createSessionStore(
      fakeFetch({
        "POST /api/auth/login": () => json(200, sessionInfo({ csrfToken: "c-1" })),
        "GET /api/session": () => json(200, sessionInfo({ csrfToken: "c-2" })),
      }),
    );
    await s.login("alice", "pw");
    const { csrfToken, refresh, unauthorized } = s;
    expect(csrfToken()).toBe("c-1");
    await refresh();
    expect(csrfToken()).toBe("c-2");
    unauthorized();
    expect(s.state()).toBe("anonymous");
    expect(s.expired()).toBe(true);
  });

  it("keeps a live session when a refresh fails with a non-401 error", async () => {
    let refreshAnswer = () => json(500, { error: { code: "internal", message: "boom" } });
    const s = createSessionStore(
      fakeFetch({
        "POST /api/auth/login": () => json(200, sessionInfo({ csrfToken: "c-1" })),
        "GET /api/session": () => refreshAnswer(),
      }),
    );
    await s.login("alice", "pw");
    await expect(s.refresh()).rejects.toMatchObject({ status: 500 });
    expect(s.state()).toBe("authenticated");
    expect(s.user()?.name).toBe("alice");
    expect(s.csrfToken()).toBe("c-1");
    expect(s.expired()).toBe(false);

    refreshAnswer = () => json(200, sessionInfo({ csrfToken: "c-2" }));
    await s.refresh();
    expect(s.csrfToken()).toBe("c-2");
  });

  it("ends up anonymous when the initial load fails with a non-401 error", async () => {
    const s = createSessionStore(
      fakeFetch({ "GET /api/session": () => json(500, { error: { code: "internal", message: "boom" } }) }),
    );
    await expect(s.load()).rejects.toMatchObject({ status: 500 });
    expect(s.state()).toBe("anonymous");
    expect(s.expired()).toBe(false);
  });

  it("drops a refresh that is answered after a newer change", async () => {
    let answerRefresh: (r: Response) => void = () => {};
    let call = 0;
    const s = createSessionStore(
      fakeFetch({
        "GET /api/session": () => {
          call++;
          if (call === 1) return json(200, sessionInfo({ admin: false, csrfToken: "c-1" }));
          return new Promise<Response>((resolve) => {
            answerRefresh = resolve;
          });
        },
        "POST /api/session/admin": () => json(200, sessionInfo({ admin: true, csrfToken: "c-2" })),
      }),
    );
    await s.load();
    const slowRefresh = s.refresh(); // e.g. on window focus; the answer is late
    await s.enableAdmin("pw");
    expect(s.admin()).toBe(true);
    answerRefresh(json(200, sessionInfo({ admin: false, csrfToken: "c-1" }))); // describes the state before admin mode
    await slowRefresh;
    expect(s.admin()).toBe(true);
    expect(s.csrfToken()).toBe("c-2");
  });

  it("drops an older refresh that is answered after a newer one", async () => {
    const { held, route } = heldSessionAnswers();
    const s = createSessionStore(
      fakeFetch({
        "POST /api/auth/login": () => json(200, sessionInfo({ csrfToken: "c-0" })),
        "GET /api/session": route,
      }),
    );
    await s.login("alice", "pw");
    const older = s.refresh();
    const newer = s.refresh();
    held[1]!(json(200, sessionInfo({ csrfToken: "c-new" })));
    await newer;
    held[0]!(json(200, sessionInfo({ csrfToken: "c-old" })));
    await older;
    expect(s.csrfToken()).toBe("c-new");
  });

  it("does not bring a session back that a logout ended while a refresh was on its way", async () => {
    const { held, route } = heldSessionAnswers();
    const s = createSessionStore(
      fakeFetch({
        "POST /api/auth/login": () => json(200, sessionInfo()),
        "POST /api/auth/logout": () => new Response(null, { status: 204 }),
        "GET /api/session": route,
      }),
    );
    await s.login("alice", "pw");
    const slowRefresh = s.refresh();
    await s.logout();
    held[0]!(json(200, sessionInfo())); // sent while the user was still signed in
    await slowRefresh;
    expect(s.state()).toBe("anonymous");
    expect(s.user()).toBeUndefined();
  });

  it("does not bring a session back that ended while a refresh was on its way", async () => {
    const { held, route } = heldSessionAnswers();
    const s = createSessionStore(
      fakeFetch({ "POST /api/auth/login": () => json(200, sessionInfo()), "GET /api/session": route }),
    );
    await s.login("alice", "pw");
    const slowRefresh = s.refresh();
    s.unauthorized(); // another request found out that the session is gone
    held[0]!(json(200, sessionInfo()));
    await slowRefresh;
    expect(s.state()).toBe("anonymous");
    expect(s.expired()).toBe(true);
    expect(s.user()).toBeUndefined();
  });

  it("keeps the user signed in when a refresh from before the admin switch is answered 401", async () => {
    const { held, route } = heldSessionAnswers();
    const s = createSessionStore(
      fakeFetch({
        "POST /api/auth/login": () => json(200, sessionInfo({ csrfToken: "c-1" })),
        "POST /api/session/admin": () => json(200, sessionInfo({ admin: true, csrfToken: "c-2" })),
        "GET /api/session": route,
      }),
    );
    await s.login("alice", "pw");
    const slowRefresh = s.refresh(); // sent with the cookie of the session before admin mode
    await s.enableAdmin("pw");
    held[0]!(sessionGone());
    await slowRefresh;
    expect(s.state()).toBe("authenticated");
    expect(s.admin()).toBe(true);
    expect(s.expired()).toBe(false);
  });

  it("ends the session when the newest refresh is answered 401", async () => {
    const s = createSessionStore(
      fakeFetch({
        "POST /api/auth/login": () => json(200, sessionInfo()),
        "GET /api/session": sessionGone,
      }),
    );
    await s.login("alice", "pw");
    await s.refresh();
    expect(s.state()).toBe("anonymous");
    expect(s.expired()).toBe(true);
  });

  it("ends up anonymous when the initial load is answered 401 without the core's code", async () => {
    const s = createSessionStore(
      fakeFetch({ "GET /api/session": () => new Response(null, { status: 401 }) }),
    );
    await expect(s.load()).rejects.toMatchObject({ status: 401 });
    expect(s.state()).toBe("anonymous");
    expect(s.expired()).toBe(false);
  });

  it("lets every request retry with the fresh token when refreshes overlap", async () => {
    const { held, route } = heldSessionAnswers();
    const checkToken = (req: FakeRequest) =>
      req.headers["X-Csrf-Token"] === "c-new"
        ? json(200, { done: true })
        : json(403, { error: { code: "csrf_invalid", message: "stale token" } });
    const fetch = fakeFetch({
      "POST /api/auth/login": () => json(200, sessionInfo({ csrfToken: "c-old" })),
      "GET /api/session": route,
      "PUT /api/a": checkToken,
      "PUT /api/b": checkToken,
    });
    const s = createSessionStore(fetch);
    await s.login("alice", "pw");
    const puts = () => fetch.calls.filter((c) => c.method === "PUT");

    // two requests with the stale token: both answered 403, both ask for a fresh session
    const outcomes = Promise.allSettled([s.api.put("/a", {}), s.api.put("/b", {})]);
    await vi.waitFor(() => expect(held).toHaveLength(2));
    held[0]!(json(200, sessionInfo({ csrfToken: "c-mid" }))); // dropped, the second load is newer
    await settle();
    expect(puts()).toHaveLength(2); // request a waits for the newest load: it brings the token
    held[1]!(json(200, sessionInfo({ csrfToken: "c-new" })));

    expect(await outcomes).toEqual([
      { status: "fulfilled", value: { done: true } },
      { status: "fulfilled", value: { done: true } },
    ]);
    expect(puts().map((c) => c.headers["X-Csrf-Token"])).toEqual(["c-old", "c-old", "c-new", "c-new"]);
    expect(s.csrfToken()).toBe("c-new");
  });

  it("changes state and user together, so consumers never see a half-updated session", async () => {
    const s = createSessionStore(
      fakeFetch({
        "POST /api/auth/login": () => json(200, sessionInfo()),
        "POST /api/auth/logout": () => new Response(null, { status: 204 }),
        "GET /api/session": () => json(200, sessionInfo({ csrfToken: "c-3" })),
      }),
    );
    const seen: [state: string, user: string | undefined][] = [];
    const dispose = createRoot((dispose) => {
      createEffect(() => seen.push([s.state(), s.user()?.name]));
      return dispose;
    });
    await s.login("alice", "pw");
    s.unauthorized();
    await s.login("alice", "pw");
    await s.logout();
    await s.load();
    dispose();

    expect(seen.map(([state]) => state)).toEqual([
      "loading",
      "authenticated",
      "anonymous",
      "authenticated",
      "anonymous",
      "authenticated",
    ]);
    for (const [state, user] of seen) expect(state === "authenticated").toBe(user !== undefined);
  });
});
