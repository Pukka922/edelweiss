import { type ApiClient, ApiError, createApiClient, type SessionApi, type SessionInfo } from "@edelweiss/sdk";
import { type Accessor, batch, createSignal } from "solid-js";

export type SessionState = "loading" | "authenticated" | "anonymous";

export interface SessionStore extends SessionApi {
  /** Client for the core API (/api). */
  api: ApiClient;
  state: Accessor<SessionState>;
  /** True after a session ended unexpectedly (an API call answered 401). */
  expired: Accessor<boolean>;
  load(): Promise<void>;
  login(username: string, password: string): Promise<void>;
  logout(): Promise<void>;
  enableAdmin(password: string): Promise<void>;
  disableAdmin(): Promise<void>;
  /** True while requestAdmin() waits for the admin dialog. */
  adminRequested: Accessor<boolean>;
  answerAdminRequest(enabled: boolean): void;
}

export function createSessionStore(fetchImpl?: typeof fetch): SessionStore {
  const [info, setInfo] = createSignal<SessionInfo>();
  const [state, setState] = createSignal<SessionState>("loading");
  const [expired, setExpired] = createSignal(false);
  const [waiting, setWaiting] = createSignal<((enabled: boolean) => void)[]>([]);
  // Bumped by every change of the session data. A load() answered after a newer change (login, logout, admin
  // on/off, unauthorized, another load) describes an older state and is dropped; otherwise a slow refresh on
  // window focus could, for example, undo admin mode in the UI.
  let generation = 0;

  const answerAdminRequest = (enabled: boolean) => {
    const resolvers = waiting();
    setWaiting([]);
    for (const resolve of resolvers) resolve(enabled);
  };

  // Several signals change together; batch() keeps consumers from seeing state() "authenticated" without a user.
  const unauthorized = () =>
    batch(() => {
      generation++;
      if (state() === "authenticated") setExpired(true);
      setInfo(undefined);
      setState("anonymous");
      answerAdminRequest(false);
    });

  const api = createApiClient({
    base: "/api",
    csrfToken: () => info()?.csrfToken,
    onUnauthorized: unauthorized,
    refreshCsrf: () => load(),
    fetch: fetchImpl,
  });

  // /session is read by a client that does not report 401 by itself: load() decides, because only an answer that is
  // still current may end the session (a refresh sent with the old cookie just before an admin switch must not).
  const sessionApi = createApiClient({ base: "/api", csrfToken: () => undefined, fetch: fetchImpl });

  // The newest load() and the generation it started. A dropped load waits for it, see newerLoad().
  let newestLoad: { generation: number; done: Promise<void> } | undefined;

  function load(): Promise<void> {
    const mine = ++generation;
    const done = fetchSession(mine);
    newestLoad = { generation: mine, done };
    return done;
  }

  async function fetchSession(mine: number): Promise<void> {
    try {
      const s = await sessionApi.get<SessionInfo>("/session");
      if (mine === generation) {
        batch(() => {
          setInfo(s);
          setState("authenticated");
        });
        return;
      }
    } catch (err) {
      if (mine === generation) {
        if (err instanceof ApiError && err.status === 401 && err.code === "unauthenticated") {
          unauthorized();
          return;
        }
        // Only the initial load gives up (any other 401 included); a failed refresh must not end a session that is
        // still alive.
        if (state() === "loading") setState("anonymous");
        throw err;
      }
    }
    // The answer, or the failure, describes an older state: it is dropped.
    await newerLoad();
  }

  // A dropped load waits for the load that overtook it, if that is the newest change and still on its way: whoever
  // waits for load() (the CSRF retry of the API client) continues after it and has to find the fresh token. Login,
  // logout, admin on/off and unauthorized have applied their data already, there is nothing to wait for.
  async function newerLoad(): Promise<void> {
    if (newestLoad?.generation === generation) await newestLoad.done.catch(() => {});
  }

  async function login(username: string, password: string) {
    const s = await api.post<SessionInfo>("/auth/login", { username, password });
    generation++;
    batch(() => {
      setInfo(s);
      setExpired(false);
      setState("authenticated");
    });
  }

  async function logout() {
    try {
      await api.post("/auth/logout");
    } finally {
      generation++;
      batch(() => {
        setInfo(undefined);
        setExpired(false);
        setState("anonymous");
        answerAdminRequest(false);
      });
    }
  }

  async function enableAdmin(password: string) {
    const s = await api.post<SessionInfo>("/session/admin", { password });
    generation++;
    batch(() => {
      setInfo(s);
      answerAdminRequest(s.admin);
    });
  }

  async function disableAdmin() {
    const s = await api.del<SessionInfo>("/session/admin");
    generation++;
    setInfo(s);
  }

  function requestAdmin(): Promise<boolean> {
    if (info()?.admin) return Promise.resolve(true);
    if (!info()?.canAdmin) return Promise.resolve(false);
    return new Promise((resolve) => setWaiting((list) => [...list, resolve]));
  }

  // Closures only, no `this`: the SDK's useApi() hands csrfToken/unauthorized/refresh around unbound.
  return {
    api,
    info,
    user: () => info()?.user,
    admin: () => info()?.admin ?? false,
    canAdmin: () => info()?.canAdmin ?? false,
    csrfToken: () => info()?.csrfToken,
    requestAdmin,
    refresh: load,
    unauthorized,
    state,
    expired,
    load,
    login,
    logout,
    enableAdmin,
    disableAdmin,
    adminRequested: () => waiting().length > 0,
    answerAdminRequest,
  };
}
