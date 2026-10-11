import { createSignal } from "solid-js";
import type { SessionApi } from "./session";
import type { SessionInfo } from "./types";

export function sessionInfo(overrides: Partial<SessionInfo> = {}): SessionInfo {
  return {
    user: { name: "alice", uid: 1000, groups: ["alice", "wheel"] },
    admin: false,
    canAdmin: true,
    csrfToken: "csrf-1",
    hostname: "alpine",
    version: "0.1.0",
    ...overrides,
  };
}

/** A SessionApi backed by a signal, for tests. */
export function fakeSession(initial: Partial<SessionInfo> = {}) {
  const [info, setInfo] = createSignal<SessionInfo | undefined>(sessionInfo(initial));
  const calls = { requestAdmin: 0, refresh: 0, unauthorized: 0 };
  const api: SessionApi = {
    info,
    user: () => info()?.user,
    admin: () => info()?.admin ?? false,
    canAdmin: () => info()?.canAdmin ?? false,
    csrfToken: () => info()?.csrfToken,
    requestAdmin: async () => {
      calls.requestAdmin++;
      return info()?.admin ?? false;
    },
    refresh: async () => {
      calls.refresh++;
    },
    unauthorized: () => {
      calls.unauthorized++;
    },
  };
  return { api, setInfo, calls };
}

type Listener = (event: MessageEvent) => void;

/** Minimal EventSource stand-in (jsdom has none). */
export class FakeEventSource {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSED = 2;
  static instances: FakeEventSource[] = [];
  readonly url: string;
  readyState = FakeEventSource.CONNECTING;
  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  private listeners = new Map<string, Listener[]>();

  constructor(url: string) {
    this.url = url;
    FakeEventSource.instances.push(this);
  }
  addEventListener(type: string, listener: Listener) {
    this.listeners.set(type, [...(this.listeners.get(type) ?? []), listener]);
  }
  close() {
    this.readyState = FakeEventSource.CLOSED;
  }
  open() {
    this.readyState = FakeEventSource.OPEN;
    this.onopen?.();
  }
  emit(type: string, data: string) {
    for (const l of this.listeners.get(type) ?? []) l(new MessageEvent(type, { data }));
  }
  fail(closed: boolean) {
    this.readyState = closed ? FakeEventSource.CLOSED : FakeEventSource.CONNECTING;
    this.onerror?.();
  }
}
