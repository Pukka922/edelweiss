// @vitest-environment node
import { describe, expect, it } from "vitest";
import { checkManifest, edelweissExtension, sharedModules } from "./vite";

const valid = {
  id: "system",
  version: "0.1.0",
  apiVersion: 1,
  name: { en: "Overview" },
  menu: { section: "system", order: 10, icon: "icon.svg" },
  ui: { entry: "index.js", styles: ["style.css"], locales: "locales" },
};

describe("checkManifest", () => {
  it("accepts a valid manifest", () => {
    expect(checkManifest(valid)).toEqual([]);
  });

  it("reports every problem", () => {
    const problems = checkManifest({
      ...valid,
      id: "Bad",
      apiVersion: 2,
      name: {},
      ui: { entry: "main.js", styles: ["x.css"] },
    });
    expect(problems.join("\n")).toMatch(/id/);
    expect(problems.join("\n")).toMatch(/apiVersion/);
    expect(problems.join("\n")).toMatch(/name\.en/);
    expect(problems.join("\n")).toMatch(/ui\.entry/);
    expect(problems.join("\n")).toMatch(/ui\.styles/);
    expect(checkManifest("nope").length).toBeGreaterThan(0);
  });

  it("rejects the ids reserved for the shell and the SDK", () => {
    for (const id of ["shell", "sdk"]) {
      expect(checkManifest({ ...valid, id }).join("\n")).toMatch(new RegExp(`"${id}" is reserved`));
    }
    expect(checkManifest({ ...valid, id: "shells" })).toEqual([]);
    expect(checkManifest({ ...valid, id: "my-sdk" })).toEqual([]);
  });

  it("checks requires.apk", () => {
    expect(
      checkManifest({ ...valid, requires: { apk: ["docker", "cmd:docker", "so:libcrypto.so.3"] } }),
    ).toEqual([]);
    expect(checkManifest({ ...valid, requires: {} })).toEqual([]);
    expect(checkManifest({ ...valid, requires: null })).toEqual([]);
    const bad = checkManifest({ ...valid, requires: { apk: ["docker>=27", "docker", "docker", 7] } }).join(
      "\n",
    );
    expect(bad).toContain('requires.apk: "docker>=27" is not an apk package name');
    expect(bad).toContain('requires.apk: "docker" is listed twice');
    expect(bad).toContain("requires.apk: 7 is not an apk package name");
    expect(checkManifest({ ...valid, requires: { apk: "docker" } })).toContain(
      "requires.apk must be a list of package names",
    );
    expect(checkManifest({ ...valid, requires: ["docker"] })).toContain("requires must be an object");
    const many = Array.from({ length: 33 }, (_, i) => `pkg${i}`);
    expect(checkManifest({ ...valid, requires: { apk: many } })).toContain(
      "requires.apk must not list more than 32 packages",
    );
    expect(checkManifest({ ...valid, requires: { apk: many.slice(0, 32) } })).toEqual([]);
  });
});

describe("edelweissExtension", () => {
  it("externalises the shared runtime", () => {
    for (const s of ["solid-js", "solid-js/web", "solid-js/store", "@solidjs/router", "@edelweiss/sdk"]) {
      expect(sharedModules.some((re) => re.test(s))).toBe(true);
    }
    expect(sharedModules.some((re) => re.test("solid-icons"))).toBe(false);
    const plugins = edelweissExtension().flat();
    expect(
      plugins.some((p) => p && typeof p === "object" && "name" in p && p.name === "edelweiss-extension"),
    ).toBe(true);
  });
});
