import { type ApiClient, ApiError } from "@edelweiss/sdk";

export type UpdateResult = {
  from: string;
  to: string;
  outcome: "updated" | "rolled_back" | "rollback_failed" | "failed";
  reason?: string;
  /** lbu did not save the update: it does not survive a restart of the host. Set for everybody. */
  lbuFailed?: boolean;
  /** What lbu said; the core tells it in admin mode only. */
  lbuError?: string;
  at: string;
};

export type AboutInfo = {
  version: string;
  arch: string;
  mode: "sys" | "diskless" | "data";
  possible: boolean;
  reason?: string;
  repo: string;
  /** running: an update is in progress (from its start, the download included, until the helper ends). */
  state: { running: boolean; result?: UpdateResult };
};

export type Offer = {
  current: string;
  latest?: string;
  available: boolean;
  release?: string;
  published?: string;
  notesUrl?: string;
  asset?: string;
  size?: number;
  sha256?: string;
};

export type CheckView = {
  offer: Offer;
  /** When the core asked GitHub (RFC 3339), and how old that is by the clock of the core. */
  checkedAt: string;
  ageSeconds: number;
  /** GitHub did not answer: the offer is that of the last good check. */
  stale?: boolean;
  staleCode?: string;
  staleReason?: string;
  /** The new binary would not be saved by lbu: the update is lost at the next restart of the host. */
  lbuWarning?: boolean;
};

/** The core's self-update routes (/api/about/…); the check, the update and dismissing need admin mode. */
export interface AboutApi {
  info(): Promise<AboutInfo>;
  /** Without refresh the core may answer from its cache (2 minutes); with refresh it asks GitHub. */
  check(refresh?: boolean): Promise<CheckView>;
  /** Starts the update to the offer with this checksum; the core refuses with release_changed if the offer moved on. */
  start(sha256: string): Promise<{ to: string }>;
  /** Forgets the result of the last update. */
  dismiss(): Promise<void>;
}

export function createAboutApi(api: ApiClient): AboutApi {
  return {
    info: () => api.get<AboutInfo>("/about"),
    check: (refresh) => api.get<CheckView>(`/about/update${refresh ? "?refresh=1" : ""}`),
    start: (sha256) => api.post<{ to: string }>("/about/update", { sha256 }),
    dismiss: () => api.del<void>("/about/update/result"),
  };
}

export const DEV_VERSION = "0.0.0-dev";

const KNOWN_CODES = new Set([
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
]);

/** The message key for an error code of the about routes (about.errors.<code>), or the generic one. */
export function aboutErrorKey(code: string): string {
  return KNOWN_CODES.has(code) ? `about.errors.${code}` : "about.errors.generic";
}

const KNOWN_REASONS = new Set([
  "dev_mode",
  "dev_build",
  "not_service",
  "no_openrc",
  "not_writable",
  "unsupported_arch",
  "unknown_mode",
  "no_lbu",
  "listen_address",
]);
const KNOWN_RESULT_REASONS = new Set([
  "not_healthy",
  "unstable",
  "restart_failed",
  "swap_failed",
  "internal_error",
  "bad_plan",
  "no_staged",
  "backup_failed",
  "no_lock",
  "result_unreadable",
]);
const KNOWN_OUTCOMES = new Set(["updated", "rolled_back", "rollback_failed", "failed"]);

type T = (key: string, params?: Record<string, string | number>) => string;

/** Why self-update is not possible here, in words; a reason that is not known shows as its code. */
export function reasonText(reason: string, t: T): string {
  return KNOWN_REASONS.has(reason) ? t(`about.reasons.${reason}`) : reason;
}

/** Why the helper ended an update as it did, in words; a reason that is not known shows as its code. */
export function resultReasonText(reason: string, t: T): string {
  return KNOWN_RESULT_REASONS.has(reason) ? t(`about.reasons.${reason}`) : reason;
}

/** The key of the outcome's text, or undefined for an outcome that is not known. */
export function outcomeKey(outcome: string): string | undefined {
  return KNOWN_OUTCOMES.has(outcome) ? `about.outcomes.${outcome}` : undefined;
}

/** An error of the about routes in words: per code, not_possible with its reason. */
export function aboutMessage(err: unknown, t: T): string {
  if (!(err instanceof ApiError)) return String(err);
  const reason = typeof err.details.reason === "string" ? reasonText(err.details.reason, t) : "";
  return t(aboutErrorKey(err.code), { message: err.message, reason });
}
