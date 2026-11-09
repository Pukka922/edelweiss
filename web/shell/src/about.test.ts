import { ApiError, createApiClient } from "@edelweiss/sdk";
import { describe, expect, it } from "vitest";
import { aboutErrorKey, aboutMessage, createAboutApi } from "./about";
import { type FakeRequest, fakeFetch, json } from "./test/fake-fetch";

const SUM = "ab".repeat(32);

function setup() {
  const fetch = fakeFetch({
    "GET /api/about": () => json(200, { version: "0.3.1", state: { running: false } }),
    "GET /api/about/update": () => json(200, { offer: { current: "0.3.1", available: false } }),
    "GET /api/about/update?refresh=1": () => json(200, { offer: { current: "0.3.1", available: false } }),
    "POST /api/about/update": () => json(202, { to: "0.4.0" }),
    "DELETE /api/about/update/result": () => json(204),
  });
  const api = createApiClient({ base: "/api", csrfToken: () => "tok", fetch });
  return { fetch, about: createAboutApi(api) };
}

describe("about API", () => {
  it("reads the info and the check, with and without refresh", async () => {
    const { about, fetch } = setup();
    expect((await about.info()).version).toBe("0.3.1");
    await about.check();
    await about.check(true);
    expect(fetch.calls.map((c) => `${c.method} ${c.url}`)).toEqual([
      "GET /api/about",
      "GET /api/about/update",
      "GET /api/about/update?refresh=1",
    ]);
  });

  it("starts with the confirmed checksum and sends the CSRF token", async () => {
    const { about, fetch } = setup();
    expect(await about.start(SUM)).toEqual({ to: "0.4.0" });
    const call = fetch.calls[0] as FakeRequest;
    expect(call.method).toBe("POST");
    expect(call.body).toEqual({ sha256: SUM });
    expect(call.headers["X-Csrf-Token"]).toBe("tok");
  });

  it("dismisses the result with DELETE and the CSRF token", async () => {
    const { about, fetch } = setup();
    await about.dismiss();
    const call = fetch.calls[0] as FakeRequest;
    expect(call.method).toBe("DELETE");
    expect(call.url).toBe("/api/about/update/result");
    expect(call.headers["X-Csrf-Token"]).toBe("tok");
  });
});

describe("about errors", () => {
  it("has a text for every code of the backend and a generic one for the rest", () => {
    for (const code of [
      "not_possible",
      "no_update",
      "operation_in_progress",
      "release_changed",
      "checksum_mismatch",
      "wrong_version",
      "not_runnable",
      "bad_script",
      "no_space",
      "no_asset",
      "no_checksums",
      "checksums_disagree",
      "asset_too_large",
      "release_not_found",
      "github_rate_limited",
      "github_unavailable",
      "timeout",
      "invalid_input",
      "invalid_json",
      "self_update_unavailable",
      "admin_required",
    ]) {
      expect(aboutErrorKey(code)).toBe(`about.errors.${code}`);
    }
    expect(aboutErrorKey("something_new")).toBe("about.errors.generic");
  });

  it("words an error per code, with the reason of not_possible", () => {
    const t = (key: string, params?: Record<string, string | number>) =>
      `${key}|${Object.entries(params ?? {})
        .map(([k, v]) => `${k}=${v}`)
        .join(",")}`;
    expect(aboutMessage(new ApiError(409, "release_changed", "x"), t)).toContain(
      "about.errors.release_changed|message=x",
    );
    expect(aboutMessage(new ApiError(409, "not_possible", "x", { reason: "no_openrc" }), t)).toContain(
      "about.reasons.no_openrc|",
    );
    expect(aboutMessage(new Error("boom"), t)).toBe("Error: boom");
  });
});
