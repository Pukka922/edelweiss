import type { ExtensionInfo } from "@edelweiss/sdk";
import { json } from "./fake-fetch";

/** An /api/extensions entry for tests. */
export function ext(id: string, section: string, order: number, name = id): ExtensionInfo {
  return {
    id,
    version: "1.0.0",
    name: { en: name, de: `${name}-de` },
    menu: { section, order },
    ui: { entry: `/ext/${id}/ui/index.js`, styles: [], locales: `/ext/${id}/ui/locales` },
    requiresAdmin: false,
    requires: { apk: [] },
    missingPackages: [],
    source: { type: "file", location: `/tmp/${id}`, sha256: "abc", installedAt: "2026-10-08T00:00:00Z" },
    dev: false,
  };
}

/** The answer of GET /api/extensions/updates when everything is up to date. */
export const noUpdates = () => json(200, { updates: [], errors: [] });
