import { readFileSync } from "node:fs";
import path from "node:path";
import tailwindcss from "@tailwindcss/vite";
import type { Plugin, PluginOption, UserConfig } from "vite";
import solid from "vite-plugin-solid";
import { scopeCss } from "./css-scope";
import { extensionScopeClass } from "./src/scope";

/** Modules provided by the shell through its import map. */
export const sharedModules: RegExp[] = [
  /^solid-js$/,
  /^solid-js\/(web|store)$/,
  /^@solidjs\/router$/,
  /^@edelweiss\/sdk$/,
];

export interface ExtensionOptions {
  /** Entry module (default "src/index.tsx"). */
  entry?: string;
  /** Path of the extension's manifest.json relative to the Vite root (default "../manifest.json"). */
  manifest?: string;
}

/**
 * Vite setup for an edelweiss extension UI: library build of index.js + style.css into dist/. Every CSS file of the
 * build is scoped to the element the shell renders the extension in (see scopeCss).
 */
export function edelweissExtension(options: ExtensionOptions = {}): PluginOption[] {
  const entry = options.entry ?? "src/index.tsx";
  const manifestPath = options.manifest ?? "../manifest.json";
  let id = "";
  const plugin: Plugin = {
    name: "edelweiss-extension",
    config(_config, env): UserConfig {
      return {
        define: {
          "process.env.NODE_ENV": JSON.stringify(env.mode === "production" ? "production" : "development"),
        },
        build: {
          lib: { entry, formats: ["es"], fileName: () => "index.js", cssFileName: "style" },
          outDir: "dist",
          emptyOutDir: true,
          rolldownOptions: { external: sharedModules },
        },
      };
    },
    configResolved(resolved) {
      const file = path.resolve(resolved.root, manifestPath);
      const manifest: unknown = JSON.parse(readFileSync(file, "utf8"));
      const problems = checkManifest(manifest);
      if (problems.length > 0) throw new Error(`edelweiss: invalid ${file}:\n- ${problems.join("\n- ")}`);
      id = (manifest as { id: string }).id;
    },
  };
  const scope: Plugin = {
    name: "edelweiss-extension-css-scope",
    apply: "build",
    enforce: "post",
    // Scopes the extension's style sheet to the element the shell renders it in (see scopeCss); each build, also
    // every rebuild of vite build --watch, emits its CSS anew, so nothing is scoped twice.
    generateBundle(_options, bundle) {
      if (!id) this.error("edelweiss: the extension id is unknown (use edelweissExtension() as a whole)");
      for (const file of Object.values(bundle)) {
        if (file.type !== "asset" || !file.fileName.endsWith(".css")) continue;
        const css = typeof file.source === "string" ? file.source : new TextDecoder().decode(file.source);
        try {
          file.source = scopeCss(css, id, file.fileName);
        } catch (error) {
          // lightningcss reports where it stopped in loc
          const { message, loc } = error as { message?: string; loc?: { line: number; column: number } };
          const position = loc ? ` (line ${loc.line}, column ${loc.column})` : "";
          this.error(
            `edelweiss: cannot scope ${file.fileName} to .${extensionScopeClass(id)}: ${message ?? String(error)}${position}`,
          );
        }
      }
    },
  };
  return [solid(), tailwindcss(), plugin, scope];
}

const record = (v: unknown): Record<string, unknown> =>
  typeof v === "object" && v !== null ? (v as Record<string, unknown>) : {};

/** Extension ids that are taken: the i18n namespaces of the shell and the SDK (keep in sync with manifest.ReservedIDs in Go). */
const reservedIds = ["shell", "sdk"];

/** An apk package name or a name a package provides (keep in sync with apkNamePattern in Go's manifest package). */
const apkName = /^[A-Za-z0-9][A-Za-z0-9+._:-]{0,127}$/;
/** The most entries requires.apk may have (keep in sync with manifest.MaxRequiredPackages in Go). */
const maxRequiredPackages = 32;

function checkRequires(requires: unknown, problems: string[]) {
  if (requires === undefined || requires === null) return;
  if (typeof requires !== "object" || Array.isArray(requires)) {
    problems.push("requires must be an object");
    return;
  }
  const apk = (requires as Record<string, unknown>).apk;
  if (apk === undefined) return;
  if (!Array.isArray(apk)) {
    problems.push("requires.apk must be a list of package names");
    return;
  }
  if (apk.length > maxRequiredPackages) {
    problems.push(`requires.apk must not list more than ${maxRequiredPackages} packages`);
  }
  const seen = new Set<string>();
  for (const p of apk) {
    if (typeof p !== "string" || !apkName.test(p)) {
      problems.push(
        `requires.apk: ${JSON.stringify(p)} is not an apk package name (letters, digits and + . _ : -, no version constraint)`,
      );
    } else if (seen.has(p)) {
      problems.push(`requires.apk: "${p}" is listed twice`);
    } else {
      seen.add(p);
    }
  }
}

/** Checks the parts of manifest.json this build depends on (edelweissd validates the rest). */
export function checkManifest(value: unknown): string[] {
  const m = record(value);
  const problems: string[] = [];
  if (typeof m.id !== "string" || !/^[a-z0-9][a-z0-9-]{1,31}$/.test(m.id)) {
    problems.push("id must match ^[a-z0-9][a-z0-9-]{1,31}$");
  } else if (reservedIds.includes(m.id)) {
    problems.push(`id "${m.id}" is reserved (reserved ids: ${reservedIds.join(", ")})`);
  }
  if (m.apiVersion !== 1) problems.push("apiVersion must be 1");
  if (typeof record(m.name).en !== "string") problems.push("name.en is required");
  const ui = record(m.ui);
  if (ui.entry !== "index.js") problems.push('ui.entry must be "index.js" (the file this build produces)');
  const styles = ui.styles;
  if (styles !== undefined && !(Array.isArray(styles) && styles.every((s) => s === "style.css"))) {
    problems.push('ui.styles may only contain "style.css"');
  }
  checkRequires(m.requires, problems);
  return problems;
}
