// @vitest-environment node
import { createHash } from "node:crypto";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import {
  contentHash,
  importMap,
  packageDir,
  resolveVendor,
  sharedSpecifiers,
  vendorModules,
  vendorURL,
  watchTarget,
} from "./vendor";

const shellDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");

/** A throwaway project root whose node_modules holds every vendored package at the given versions. */
function fakeProject(root: string, content: (file: string) => string): void {
  const versions: Record<string, string> = {
    "solid-js": "1.9.17",
    "@solidjs/router": "1.0.0",
    "@edelweiss/sdk": "0.1.0",
  };
  for (const [pkg, version] of Object.entries(versions)) {
    const dir = path.join(root, "node_modules", pkg);
    mkdirSync(dir, { recursive: true });
    writeFileSync(path.join(dir, "package.json"), JSON.stringify({ name: pkg, version }));
  }
  for (const m of vendorModules) {
    const file = path.join(root, "node_modules", m.pkg, m.file);
    mkdirSync(path.dirname(file), { recursive: true });
    writeFileSync(file, content(`${m.pkg}/${m.file}`));
  }
}

describe("vendor", () => {
  it("hashes content to the first 8 hex digits of its SHA-256", () => {
    const bytes = Buffer.from("export const x = 1;\n");
    expect(contentHash(bytes)).toBe(createHash("sha256").update(bytes).digest("hex").slice(0, 8));
    expect(contentHash(bytes)).toMatch(/^[0-9a-f]{8}$/);
    expect(contentHash(Buffer.from("export const x = 2;\n"))).not.toBe(contentHash(bytes));
  });

  it("builds versioned, content-hashed vendor URLs", () => {
    const bytes = Buffer.from("abc"); // sha256: ba7816bf...
    expect(vendorURL("solid-js", "1.9.17", "web/dist/web.js", bytes)).toBe(
      "/vendor/solid-js@1.9.17-ba7816bf/web.js",
    );
    expect(vendorURL("@solidjs/router", "1.0.0", "dist/index.js", bytes)).toBe(
      "/vendor/@solidjs/router@1.0.0-ba7816bf/index.js",
    );
  });

  it("serialises the import map", () => {
    expect(JSON.parse(importMap([{ specifier: "solid-js", url: "/vendor/s.js" }]))).toEqual({
      imports: { "solid-js": "/vendor/s.js" },
    });
  });

  it("finds packages, including ones without a package.json export", () => {
    for (const pkg of ["solid-js", "@solidjs/router", "@edelweiss/sdk"]) {
      const dir = packageDir(pkg, shellDir);
      expect(JSON.parse(readFileSync(path.join(dir, "package.json"), "utf8")).name).toBe(pkg);
    }
    expect(() => packageDir("does-not-exist-xyz", shellDir)).toThrow(/not found/);
  });

  describe("resolveVendor", () => {
    let root: string;
    beforeEach(() => {
      root = mkdtempSync(path.join(tmpdir(), "edelweiss-vendor-"));
    });
    afterEach(() => {
      rmSync(root, { recursive: true, force: true });
    });

    it("resolves every shared specifier to an existing runtime file at a hashed URL", () => {
      fakeProject(root, (file) => `// ${file}\n`);
      const entries = resolveVendor(root);
      expect(entries.map((e) => e.specifier)).toEqual(sharedSpecifiers);
      for (const e of entries) {
        expect(existsSync(e.source)).toBe(true);
        expect(e.url).toMatch(/^\/vendor\/.+@\d+\.\d+\.\d+-[0-9a-f]{8}\/[a-z]+\.js$/);
        expect(e.url).toContain(`-${contentHash(readFileSync(e.source))}/`);
        expect(Buffer.compare(e.content, readFileSync(e.source))).toBe(0);
      }
    });

    it("changes the URL when the file content changes and keeps it while it does not", () => {
      fakeProject(root, (file) => `// ${file}\n`);
      const urls = () => Object.fromEntries(resolveVendor(root).map((e) => [e.specifier, e.url]));
      const before = urls();
      expect(urls()).toEqual(before);

      const sdk = path.join(root, "node_modules", "@edelweiss/sdk", "dist/index.js");
      writeFileSync(sdk, "// a new sdk build\n");
      const after = urls();
      expect(after["@edelweiss/sdk"]).not.toBe(before["@edelweiss/sdk"]);
      expect({ ...after, "@edelweiss/sdk": "" }).toEqual({ ...before, "@edelweiss/sdk": "" });
    });

    it("names the missing file", () => {
      fakeProject(root, (file) => `// ${file}\n`);
      rmSync(path.join(root, "node_modules", "@edelweiss/sdk", "dist/index.js"));
      expect(() => resolveVendor(root)).toThrow(/index\.js is missing \(build @edelweiss\/sdk first\)/);
    });
  });

  it("watches the file, or the nearest existing ancestor while it is missing", () => {
    const root = mkdtempSync(path.join(tmpdir(), "edelweiss-vendor-"));
    try {
      const file = path.join(root, "dist", "index.js");
      expect(watchTarget(file)).toBe(root);
      mkdirSync(path.dirname(file));
      expect(watchTarget(file)).toBe(path.dirname(file));
      writeFileSync(file, "");
      expect(watchTarget(file)).toBe(file);
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  });
});
