import { type Accessor, createSignal, onCleanup } from "solid-js";
import { useExtension } from "./extension";

export interface Socket {
  status: Accessor<"connecting" | "open" | "closed">;
  /** Last message received. */
  last: Accessor<MessageEvent | undefined>;
  /** Same payloads as WebSocket.send. */
  send(data: Parameters<WebSocket["send"]>[0]): void;
  close(): void;
}

/** WebSocket to the current extension's backend; closed when the owner is disposed. */
export function useSocket(path: string): Socket {
  const ext = useExtension();
  const scheme = window.location.protocol === "https:" ? "wss:" : "ws:";
  const ws = new WebSocket(
    `${scheme}//${window.location.host}${ext.apiBase}${path.startsWith("/") ? path : `/${path}`}`,
  );
  const [status, setStatus] = createSignal<"connecting" | "open" | "closed">("connecting");
  const [last, setLast] = createSignal<MessageEvent>();
  ws.onopen = () => setStatus("open");
  ws.onclose = () => setStatus("closed");
  ws.onmessage = (e) => setLast(() => e);
  onCleanup(() => ws.close());
  return { status, last, send: (data) => ws.send(data), close: () => ws.close() };
}
