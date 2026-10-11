import { type Accessor, createEffect, createSignal, on, onCleanup, useContext } from "solid-js";
import { useExtension } from "./extension";
import { SessionContext } from "./session";

export type StreamStatus = "connecting" | "open" | "error";

export interface Stream<T> {
  /** Latest event payload (JSON-decoded). */
  data: Accessor<T | undefined>;
  status: Accessor<StreamStatus>;
}

/** Subscribes to Server-Sent Events at url; closes when the owner is disposed. */
export function openStream<T>(url: string, event: string): Stream<T> {
  const [data, setData] = createSignal<T>();
  const [status, setStatus] = createSignal<StreamStatus>("connecting");
  const source = new EventSource(url);
  source.onopen = () => setStatus("open");
  source.onerror = () => setStatus(source.readyState === EventSource.CLOSED ? "error" : "connecting");
  source.addEventListener(event, (e) => {
    try {
      const value = JSON.parse((e as MessageEvent<string>).data) as T;
      setData(() => value);
    } catch {
      // ignore malformed events
    }
  });
  onCleanup(() => source.close());
  return { data, status };
}

/** Server-Sent Events from the current extension's backend, e.g. useStream("/metrics", { event: "metrics" }). */
export function useStream<T>(path: string, options: { event?: string } = {}): Stream<T> {
  const ext = useExtension();
  const session = useContext(SessionContext);
  const stream = openStream<T>(
    `${ext.apiBase}${path.startsWith("/") ? path : `/${path}`}`,
    options.event ?? "message",
  );
  // A stream the browser gave up on may mean the session expired; EventSource cannot see the status code, so ask
  // the core. If the session is gone, the shell switches to the login page.
  createEffect(
    on(
      stream.status,
      (status) => {
        if (status === "error") void session?.refresh().catch(() => {});
      },
      { defer: true },
    ),
  );
  return stream;
}
