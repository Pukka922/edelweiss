// @vitest-environment node
import type { Plugin } from "vite";
import { describe, expect, it } from "vitest";
import { checkManifest, type ExtensionOptions, edelweissExtension, sharedModules } from "./vite";

/** Looks up the plugins of one edelweissExtension() call by name. */
function pluginsByName(options?: ExtensionOptions) {
  const plugins = edelweissExtension(options).flat();
  return (name: string) =>
    plugins.find((p) => p && typeof p === "object" && "name" in p && p.name === name) as Plugin;
}

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

  it("scopes every CSS asset of each build and leaves JS alone", () => {
    const named = pluginsByName({ manifest: "../../scripts/sdk-pack-check/manifest.json" });
    const scope = named("edelweiss-extension-css-scope");
    expect(scope.apply).toBe("build");
    expect(scope.enforce).toBe("post");
    (named("edelweiss-extension").configResolved as (c: { root: string }) => void)({ root: __dirname });
    // vite build --watch runs the hook again with a new bundle for every rebuild
    for (let build = 0; build < 2; build++) {
      const bundle = {
        "style.css": { type: "asset", fileName: "style.css", source: ".p-4{color:red}" },
        "extra.css": {
          type: "asset",
          fileName: "extra.css",
          source: new TextEncoder().encode(".a{color:red}"),
        },
        "index.js": { type: "chunk", fileName: "index.js", code: "export {}" },
      };
      (scope.generateBundle as unknown as (o: unknown, b: typeof bundle) => void)({}, bundle);
      expect(bundle["style.css"].source).toBe(":where(.edelweiss-ext-probe) .p-4{color:red}");
      expect(bundle["extra.css"].source).toBe(":where(.edelweiss-ext-probe) .a{color:red}");
      expect(bundle["index.js"].code).toBe("export {}");
    }
  });

  it("refuses to scope when the extension id is unknown", () => {
    const scope = pluginsByName()("edelweiss-extension-css-scope"); // configResolved of the other plugin never ran
    const bundle = { "style.css": { type: "asset", fileName: "style.css", source: ".p-4{color:red}" } };
    // Rollup's this.error throws
    const error = (message: string): never => {
      throw new Error(message);
    };
    const generate = scope.generateBundle as unknown as (
      this: { error: typeof error },
      o: unknown,
      b: object,
    ) => void;
    expect(() => generate.call({ error }, {}, bundle)).toThrow("the extension id is unknown");
    expect(bundle["style.css"].source).toBe(".p-4{color:red}");
  });

  it("names the file, the container and the position when a style sheet cannot be scoped", () => {
    const named = pluginsByName({ manifest: "../../scripts/sdk-pack-check/manifest.json" });
    (named("edelweiss-extension").configResolved as (c: { root: string }) => void)({ root: __dirname });
    const broken = ".ok{color:red}\n..a{color:red}";
    const bundle = { "style.css": { type: "asset", fileName: "style.css", source: broken } };
    // Rollup's this.error throws
    const error = (message: string): never => {
      throw new Error(message);
    };
    const generate = named("edelweiss-extension-css-scope").generateBundle as unknown as (
      this: { error: typeof error },
      o: unknown,
      b: object,
    ) => void;
    expect(() => generate.call({ error }, {}, bundle)).toThrow(
      "edelweiss: cannot scope style.css to .edelweiss-ext-probe: " +
        "Expected identifier in class selector, got Delim('.') (line 2, column 2)",
    );
    expect(bundle["style.css"].source).toBe(broken);
  });
});
