/** An error answered by edelweissd or an extension: {"error":{"code","message", …further fields}}. */
export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  /** The further fields of the error object besides code and message, e.g. {packages: ["docker"]}. */
  readonly details: Record<string, unknown>;

  constructor(status: number, code: string, message: string, details: Record<string, unknown> = {}) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
    this.details = details;
  }
}

export interface ApiClientOptions {
  /** Base path, e.g. "/api" or "/ext/system/api". */
  base: string;
  /** Current CSRF token (sent on unsafe methods). */
  csrfToken: () => string | undefined;
  /** Called when the core reports that the session is gone (401 with code "unauthenticated"). */
  onUnauthorized?: () => void;
  /** Called once on 403 csrf_invalid; the request is then retried once. */
  refreshCsrf?: () => Promise<void>;
  fetch?: typeof fetch;
}

export interface ApiClient {
  get<T>(path: string): Promise<T>;
  post<T>(path: string, body?: unknown): Promise<T>;
  put<T>(path: string, body?: unknown): Promise<T>;
  patch<T>(path: string, body?: unknown): Promise<T>;
  del<T>(path: string): Promise<T>;
  /** Absolute URL of path below the base (for EventSource, links). */
  url(path: string): string;
}

const SAFE_METHODS = new Set(["GET", "HEAD"]);

export function createApiClient(options: ApiClientOptions): ApiClient {
  const doFetch: typeof fetch = options.fetch ?? ((input, init) => fetch(input, init));
  const base = options.base.replace(/\/+$/, "");
  const url = (path: string) => `${base}${path.startsWith("/") ? path : `/${path}`}`;

  async function send<T>(method: string, path: string, body: unknown, retried: boolean): Promise<T> {
    const headers: Record<string, string> = { Accept: "application/json" };
    if (body !== undefined) headers["Content-Type"] = "application/json";
    if (!SAFE_METHODS.has(method)) {
      const token = options.csrfToken();
      if (token) headers["X-Csrf-Token"] = token;
    }
    const res = await doFetch(url(path), {
      method,
      headers,
      credentials: "same-origin",
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    if (res.ok) {
      if (res.status === 204) return undefined as T;
      const text = await res.text();
      return (text ? JSON.parse(text) : undefined) as T;
    }
    const err = await toApiError(res);
    // Only the core's "unauthenticated" means the session is gone. A wrong password is invalid_credentials, and an
    // extension may answer 401 for reasons of its own (its own tokens, for example) without signing anybody out.
    if (err.status === 401 && err.code === "unauthenticated") options.onUnauthorized?.();
    if (err.status === 403 && err.code === "csrf_invalid" && !retried && options.refreshCsrf) {
      await options.refreshCsrf();
      return send<T>(method, path, body, true);
    }
    throw err;
  }

  return {
    get: <T>(path: string) => send<T>("GET", path, undefined, false),
    post: <T>(path: string, body?: unknown) => send<T>("POST", path, body, false),
    put: <T>(path: string, body?: unknown) => send<T>("PUT", path, body, false),
    patch: <T>(path: string, body?: unknown) => send<T>("PATCH", path, body, false),
    del: <T>(path: string) => send<T>("DELETE", path, undefined, false),
    url,
  };
}

async function toApiError(res: Response): Promise<ApiError> {
  try {
    const data = (await res.json()) as { error?: Record<string, unknown> };
    const error = data?.error;
    const code = error?.code;
    if (error && typeof code === "string" && code) {
      const message = typeof error.message === "string" ? error.message : res.statusText;
      const details = Object.fromEntries(
        Object.entries(error).filter(([key]) => key !== "code" && key !== "message"),
      );
      return new ApiError(res.status, code, message, details);
    }
  } catch {
    // not a JSON error envelope
  }
  return new ApiError(res.status, `http_${res.status}`, res.statusText || `HTTP ${res.status}`);
}
