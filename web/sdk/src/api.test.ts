import { describe, expect, it, vi } from "vitest";
import { ApiError, createApiClient } from "./api";

function jsonResponse(status: number, body: unknown): Response {
  return new Response(body === undefined ? null : JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

describe("createApiClient", () => {
  it("builds URLs below the base and parses JSON", async () => {
    const fetch = vi.fn(async () => jsonResponse(200, { ok: true }));
    const api = createApiClient({ base: "/ext/system/api/", csrfToken: () => "tok", fetch });
    expect(await api.get<{ ok: boolean }>("/info")).toEqual({ ok: true });
    expect(fetch).toHaveBeenCalledWith("/ext/system/api/info", expect.objectContaining({ method: "GET" }));
    expect(api.url("metrics")).toBe("/ext/system/api/metrics");
  });

  it("sends the CSRF token and JSON body only where needed", async () => {
    const fetch = vi.fn(async () => jsonResponse(200, {}));
    const api = createApiClient({ base: "/api", csrfToken: () => "tok", fetch });
    await api.get("/session");
    await api.post("/auth/login", { username: "a" });
    const [, getInit] = fetch.mock.calls[0] as unknown as [string, RequestInit];
    const [, postInit] = fetch.mock.calls[1] as unknown as [string, RequestInit];
    expect((getInit.headers as Record<string, string>)["X-Csrf-Token"]).toBeUndefined();
    expect((postInit.headers as Record<string, string>)["X-Csrf-Token"]).toBe("tok");
    expect((postInit.headers as Record<string, string>)["Content-Type"]).toBe("application/json");
    expect(postInit.body).toBe(JSON.stringify({ username: "a" }));
  });

  it("returns undefined for 204", async () => {
    const api = createApiClient({
      base: "/api",
      csrfToken: () => "t",
      fetch: async () => new Response(null, { status: 204 }),
    });
    expect(await api.post("/auth/logout")).toBeUndefined();
  });

  it("maps the error envelope to ApiError and reports 401", async () => {
    const onUnauthorized = vi.fn();
    const api = createApiClient({
      base: "/api",
      csrfToken: () => undefined,
      onUnauthorized,
      fetch: async () => jsonResponse(401, { error: { code: "unauthenticated", message: "login required" } }),
    });
    const err = await api.get("/session").catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({ status: 401, code: "unauthenticated", message: "login required" });
    expect((err as ApiError).details).toEqual({});
    expect(onUnauthorized).toHaveBeenCalledOnce();
  });

  it("keeps the further fields of the error object as details", async () => {
    const api = createApiClient({
      base: "/api",
      csrfToken: () => "t",
      fetch: async () =>
        jsonResponse(422, {
          error: { code: "missing_packages", message: "missing packages: docker", packages: ["docker"] },
        }),
    });
    const err = await api.post("/extensions/install", {}).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({ status: 422, code: "missing_packages", message: "missing packages: docker" });
    expect((err as ApiError).details).toEqual({ packages: ["docker"] });
  });

  it("does not treat a wrong password as a lost session", async () => {
    const onUnauthorized = vi.fn();
    const api = createApiClient({
      base: "/api",
      csrfToken: () => "t",
      onUnauthorized,
      fetch: async () =>
        jsonResponse(401, { error: { code: "invalid_credentials", message: "invalid password" } }),
    });
    await expect(api.post("/session/admin", { password: "x" })).rejects.toMatchObject({
      code: "invalid_credentials",
    });
    expect(onUnauthorized).not.toHaveBeenCalled();
  });

  it("ends the session only for the core's unauthenticated code", async () => {
    for (const body of [
      { error: { code: "token_expired", message: "the extension's own token" } },
      { error: { code: "invalid_credentials", message: "wrong password" } },
      undefined, // a plain 401 without the error envelope
    ]) {
      const onUnauthorized = vi.fn();
      const api = createApiClient({
        base: "/ext/x/api",
        csrfToken: () => undefined,
        onUnauthorized,
        fetch: async () => (body ? jsonResponse(401, body) : new Response(null, { status: 401 })),
      });
      await expect(api.get("/thing")).rejects.toMatchObject({ status: 401 });
      expect(onUnauthorized).not.toHaveBeenCalled();
    }
  });

  it("falls back to http_<status> for non-JSON errors", async () => {
    const api = createApiClient({
      base: "/api",
      csrfToken: () => undefined,
      fetch: async () => new Response("bad gateway", { status: 502, statusText: "Bad Gateway" }),
    });
    await expect(api.get("/x")).rejects.toMatchObject({ status: 502, code: "http_502" });
  });

  it("refreshes the CSRF token once and retries on csrf_invalid", async () => {
    let token = "old";
    const fetch = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse(403, { error: { code: "csrf_invalid", message: "bad token" } }))
      .mockResolvedValueOnce(jsonResponse(200, { done: true }));
    const refreshCsrf = vi.fn(async () => {
      token = "new";
    });
    const api = createApiClient({ base: "/api", csrfToken: () => token, refreshCsrf, fetch });
    expect(await api.put("/thing", {})).toEqual({ done: true });
    expect(refreshCsrf).toHaveBeenCalledOnce();
    const [, retryInit] = fetch.mock.calls[1] as unknown as [string, RequestInit];
    expect((retryInit.headers as Record<string, string>)["X-Csrf-Token"]).toBe("new");
  });

  it("does not retry a second csrf_invalid", async () => {
    const fetch = vi.fn(async () => jsonResponse(403, { error: { code: "csrf_invalid", message: "bad" } }));
    const api = createApiClient({ base: "/api", csrfToken: () => "t", refreshCsrf: async () => {}, fetch });
    await expect(api.del("/x")).rejects.toMatchObject({ code: "csrf_invalid" });
    expect(fetch).toHaveBeenCalledTimes(2);
  });
});
