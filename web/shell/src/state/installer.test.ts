import { ApiError, createApiClient } from "@edelweiss/sdk";
import { describe, expect, it } from "vitest";
import { installerMessage } from "../components/InstallDialog";
import { fakeFetch, json } from "../test/fake-fetch";
import {
  apkAddCommand,
  createCheckMemory,
  createInstallerApi,
  installerErrorKey,
  missingPackagesOf,
} from "./installer";

describe("installer api", () => {
  it("calls the core routes with the CSRF token", async () => {
    const fetch = fakeFetch({
      "POST /api/extensions/resolve": () => json(200, { repo: "a/b", tag: "v1", candidates: [] }),
      "POST /api/extensions/install": () => json(201, { id: "hello" }),
      "GET /api/extensions/updates": () => json(200, { updates: [], errors: [] }),
      "GET /api/extensions/updates?refresh=1": () => json(200, { updates: [], errors: [] }),
      "POST /api/extensions/hello%20x/update": () => json(200, { id: "hello x" }),
    });
    const api = createInstallerApi(createApiClient({ base: "/api", csrfToken: () => "tok", fetch }));
    expect((await api.resolve("a/b")).tag).toBe("v1");
    await api.install("a/b@v1", "hello", "abc");
    expect((await api.updates()).updates).toEqual([]);
    expect((await api.updates(true)).updates).toEqual([]);
    expect((await api.update("hello x", "def")).id).toBe("hello x");
    expect(fetch.calls.map((c) => [c.method, c.url, c.body])).toEqual([
      ["POST", "/api/extensions/resolve", { source: "a/b" }],
      ["POST", "/api/extensions/install", { source: "a/b@v1", id: "hello", sha256: "abc" }],
      ["GET", "/api/extensions/updates", undefined],
      ["GET", "/api/extensions/updates?refresh=1", undefined],
      ["POST", "/api/extensions/hello%20x/update", { sha256: "def" }],
    ]);
    expect(fetch.calls[0]?.headers["X-Csrf-Token"]).toBe("tok");
  });

  it("maps error codes to message keys", () => {
    expect(installerErrorKey("checksum_mismatch")).toBe("installer.errors.checksum_mismatch");
    expect(installerErrorKey("something_new")).toBe("installer.errors.generic");
    expect(installerErrorKey("timeout")).toBe("installer.errors.timeout");
  });

  it("tells a check from the one before it", () => {
    const memory = createCheckMemory();
    expect(memory.isNew("2026-10-12T10:00:00Z")).toBe(true);
    expect(memory.isNew("2026-10-12T10:00:00Z")).toBe(false);
    expect(memory.isNew("2026-10-12T10:01:00Z")).toBe(true);
    expect(memory.isNew("2026-10-12T10:01:00Z")).toBe(false);
  });
});

describe("missing packages", () => {
  const t = (key: string, params?: Record<string, unknown>) => `${key} ${JSON.stringify(params ?? {})}`;

  it("reads the packages of a missing_packages error", () => {
    const err = new ApiError(422, "missing_packages", "missing packages: docker", {
      packages: ["docker", 7, "cmd:sh"],
    });
    expect(missingPackagesOf(err)).toEqual(["docker", "cmd:sh"]);
    expect(missingPackagesOf(new ApiError(422, "missing_packages", "x"))).toEqual([]);
    expect(apkAddCommand(["docker", "cmd:sh"])).toBe("apk add docker cmd:sh");
  });

  it("names the packages and the command, or falls back to the message", () => {
    const err = new ApiError(422, "missing_packages", "missing packages: docker", { packages: ["docker"] });
    expect(installerMessage(err, t)).toBe(
      'installer.errors.missing_packages {"packages":"docker","command":"apk add docker"}',
    );
    const bare = new ApiError(422, "missing_packages", "missing packages: docker");
    expect(installerMessage(bare, t)).toBe('installer.errors.generic {"message":"missing packages: docker"}');
  });
});
