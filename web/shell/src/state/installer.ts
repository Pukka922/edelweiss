import type { ApiClient, ApiError, ExtensionInfo } from "@edelweiss/sdk";

export interface Candidate {
  id: string;
  version: string;
  arch: string;
  asset: string;
  size: number;
  sha256: string;
  archive: boolean;
}

export interface Resolution {
  repo: string;
  tag: string;
  name: string;
  url: string;
  publishedAt: string;
  candidates: Candidate[];
}

export interface Update {
  id: string;
  installed: string;
  available: string;
  repo: string;
  tag: string;
  asset: string;
  sha256: string;
}

export interface UpdateCheckError {
  id: string;
  code: string;
  message: string;
}

export interface UpdateCheck {
  updates: Update[];
  errors: UpdateCheckError[];
}

/** The core's installer routes (/api/extensions/…); all of them need admin mode. */
export interface InstallerApi {
  resolve(source: string): Promise<Resolution>;
  install(source: string, id: string, sha256: string): Promise<ExtensionInfo>;
  updates(): Promise<UpdateCheck>;
  /**
   * Updates to the latest release. sha256 is the checksum the user confirmed; the core refuses with
   * 409 release_changed if the latest release has another one.
   */
  update(id: string, sha256: string): Promise<ExtensionInfo>;
}

export function createInstallerApi(api: ApiClient): InstallerApi {
  return {
    resolve: (source) => api.post<Resolution>("/extensions/resolve", { source }),
    install: (source, id, sha256) => api.post<ExtensionInfo>("/extensions/install", { source, id, sha256 }),
    updates: () => api.get<UpdateCheck>("/extensions/updates"),
    update: (id, sha256) =>
      api.post<ExtensionInfo>(`/extensions/${encodeURIComponent(id)}/update`, { sha256 }),
  };
}

const KNOWN_CODES = new Set([
  "invalid_source",
  "release_not_found",
  "no_matching_assets",
  "checksums_missing",
  "checksum_mismatch",
  "release_changed",
  "asset_too_large",
  "github_rate_limited",
  "github_unavailable",
  "operation_in_progress",
  "invalid_extension",
  "id_required",
  "unknown_id",
  "up_to_date",
]);

/** The message key for an installer error code (installer.errors.<code>), or the generic one. */
export function installerErrorKey(code: string): string {
  return KNOWN_CODES.has(code) ? `installer.errors.${code}` : "installer.errors.generic";
}

/** The packages of a missing_packages error (its "packages" field); empty if it has none. */
export function missingPackagesOf(err: ApiError): string[] {
  const p = err.details.packages;
  return Array.isArray(p) ? p.filter((x): x is string => typeof x === "string") : [];
}

/** The command that installs packages, run as root. */
export function apkAddCommand(packages: string[]): string {
  return `apk add ${packages.join(" ")}`;
}
