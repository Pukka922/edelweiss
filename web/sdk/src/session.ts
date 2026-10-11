import { type Accessor, createContext, useContext } from "solid-js";
import type { SessionInfo, SessionUser } from "./types";

/** The logged-in session as provided by the shell. */
export interface SessionApi {
  info: Accessor<SessionInfo | undefined>;
  user: Accessor<SessionUser | undefined>;
  admin: Accessor<boolean>;
  canAdmin: Accessor<boolean>;
  csrfToken: Accessor<string | undefined>;
  /** Opens the shell's admin dialog; resolves true once admin mode is on. */
  requestAdmin(): Promise<boolean>;
  /** Reloads /api/session (e.g. after the CSRF token rotated in another tab). */
  refresh(): Promise<void>;
  /** Tells the shell that the session is gone (an API call answered 401). */
  unauthorized(): void;
}

export const SessionContext = createContext<SessionApi>();

export function useSession(): SessionApi {
  const session = useContext(SessionContext);
  if (!session) throw new Error("useSession must be used inside the edelweiss shell");
  return session;
}
