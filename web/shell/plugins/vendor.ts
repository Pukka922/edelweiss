import { createHash } from "node:crypto";
import { existsSync, readFileSync, realpathSync } from "node:fs";
import path from "node:path";
import type { Plugin } from "vite";

/** Runtime modules shared by the shell and all extensions through the import map. */
export const vendorModules = [
  { specifier: "solid-js", pkg: "solid-js", file: "dist/solid.js" },
  { specifier: "solid-js/web", pkg: "solid-js", file: "web/dist/web.js" },
  { specifier: "solid-js/store", pkg: "solid-js", file: "store/dist/store.js" },
  { specifier: "@solidjs/router", pkg: "@solidjs/router", file: "dist/index.js" },
  { specifier: "@edelweiss/sdk", pkg: "@edelweiss/sdk", file: "dist/index.js" },
] as const;

export const sharedSpecifiers: string[] = vendorModules.map((m) => m.specifier);

/** A vendored file before it is read: where it lives and which package version it belongs to. */
export interface VendorSource {
  specifier: string;
  pkg: string;
  version: string;
  file: string;
  source: string;
}

export interface VendorEntry {
  specifier: string;
  url: string;
  source: string;
  /** The bytes the URL's hash was computed from; this is what gets served. */
  content: Uint8Array;
}

/** First 8 hex digits of the SHA-256 of the bytes. */
export function contentHash(content: Uint8Array): string {
  return createHash("sha256").update(content).digest("hex").slice(0, 8);
}

/** `/vendor/<package>@<version>-<sha8>/<basename>`: the hash makes the URL change with the content, so it can be cached forever. */
export function vendorURL(pkg: string, version: string, file: string, content: Uint8Array): string {
  return `/vendor/${pkg}@${version}-${contentHash(content)}/${path.posix.basename(file)}`;
}

export function importMap(entries: readonly { specifier: string; url: string }[]): string {
  return JSON.stringify({ imports: Object.fromEntries(entries.map((e) => [e.specifier, e.url])) });
}

/** Directory of an installed package, searching node_modules upwards like Node does. */
export function packageDir(pkg: string, fromDir: string): string {
  for (let dir = path.resolve(fromDir); ; dir = path.dirname(dir)) {
    const candidate = path.join(dir, "node_modules", pkg);
    if (existsSync(path.join(candidate, "package.json"))) return realpathSync(candidate);
    if (path.dirname(dir) === dir) throw new Error(`vendor: package ${pkg} not found from ${fromDir}`);
  }
}

/** Locates the vendored files without reading them. */
export function vendorSources(root: string): VendorSource[] {
  return vendorModules.map((m) => {
    const dir = packageDir(m.pkg, root);
    const { version } = JSON.parse(readFileSync(path.join(dir, "package.json"), "utf8")) as {
      version: string;
    };
    return { specifier: m.specifier, pkg: m.pkg, version, file: m.file, source: path.join(dir, m.file) };
  });
}

/** Reads and hashes the files; throws if one is missing (the SDK has to be built first). */
export function readVendor(sources: readonly VendorSource[]): VendorEntry[] {
  return sources.map((s) => {
    if (!existsSync(s.source)) throw new Error(`vendor: ${s.source} is missing (build @edelweiss/sdk first)`);
    const content = readFileSync(s.source);
    return {
      specifier: s.specifier,
      url: vendorURL(s.pkg, s.version, s.file, content),
      source: s.source,
      content,
    };
  });
}

export function resolveVendor(root: string): VendorEntry[] {
  return readVendor(vendorSources(root));
}

/** The path itself, or its nearest existing ancestor: the watcher cannot watch a path that does not exist yet. */
export function watchTarget(file: string): string {
  let target = file;
  while (!existsSync(target) && path.dirname(target) !== target) target = path.dirname(target);
  return target;
}

/**
 * Copies the shared runtime to dist/vendor/<package>@<version>-<sha8>/ and injects the import map into <head>, ahead
 * of the module script. The files are read once per build, in buildStart, so a watch build picks up a rebuilt SDK and
 * the URL in the import map always matches the bytes that are emitted.
 */
export function vendor(): Plugin {
  let root = process.cwd();
  let entries: VendorEntry[] = [];
  return {
    name: "edelweiss-vendor",
    configResolved(config) {
      root = config.root;
    },
    buildStart() {
      const sources = vendorSources(root);
      // Watched before the read: a failed watch build keeps watching, so it recovers once the SDK is built.
      for (const s of sources) this.addWatchFile(watchTarget(s.source));
      entries = readVendor(sources);
    },
    transformIndexHtml: {
      order: "pre",
      handler: () => [
        {
          tag: "script",
          attrs: { type: "importmap" },
          children: importMap(entries),
          injectTo: "head-prepend",
        },
      ],
    },
    generateBundle() {
      for (const e of entries) this.emitFile({ type: "asset", fileName: e.url.slice(1), source: e.content });
    },
  };
}
