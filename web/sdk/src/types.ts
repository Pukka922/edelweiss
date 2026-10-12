export type Locale = "de" | "en";
export const LOCALES: readonly Locale[] = ["de", "en"];

export const SECTIONS = ["system", "storage", "virtualization", "network", "tools", "other"] as const;
export type Section = (typeof SECTIONS)[number];

export type Localized = Partial<Record<string, string>>;

/** Picks the text for locale, falling back to English, then to any translation. */
export function localized(text: Localized | undefined, locale: Locale): string {
  if (!text) return "";
  return text[locale] ?? text.en ?? Object.values(text).find((v) => typeof v === "string") ?? "";
}

export interface SessionUser {
  name: string;
  uid: number;
  groups: string[];
}

export interface SessionInfo {
  user: SessionUser;
  admin: boolean;
  canAdmin: boolean;
  csrfToken: string;
  hostname: string;
  version: string;
}

export interface ExtensionUI {
  entry: string;
  styles: string[];
  locales?: string;
}

export interface ExtensionSource {
  type: string;
  location: string;
  sha256: string;
  installedAt: string;
  repo?: string;
  tag?: string;
  asset?: string;
}

/** One entry of GET /api/extensions. */
export interface ExtensionInfo {
  id: string;
  version: string;
  name: Localized;
  description?: Localized;
  menu: { section: string; order: number };
  icon?: string;
  ui?: ExtensionUI;
  requiresAdmin: boolean;
  /** What the extension needs on the host (the manifest's "requires"). */
  requires: { apk: string[] };
  /** The required Alpine packages that are not installed (empty if none). */
  missingPackages: string[];
  source: ExtensionSource;
  dev: boolean;
}
