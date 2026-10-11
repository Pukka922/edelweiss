import { type ApiClient, createApiClient } from "./api";
import { useExtension } from "./extension";
import { useSession } from "./session";

/** API client for the current extension's backend (/ext/<id>/api). */
export function useApi(): ApiClient {
  const ext = useExtension();
  const session = useSession();
  return createApiClient({
    base: ext.apiBase,
    csrfToken: session.csrfToken,
    // An extension backend must not end the session: ask the core instead. Its answer to /api/session decides (the
    // session ends there on a current 401 "unauthenticated"); a failed refresh leaves a live session alone.
    onUnauthorized: () => void session.refresh().catch(() => {}),
    refreshCsrf: session.refresh,
  });
}
