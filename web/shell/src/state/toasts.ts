import type { ToastApi, ToastKind } from "@edelweiss/sdk";
import { type Accessor, createSignal } from "solid-js";

export interface Toast {
  id: number;
  kind: ToastKind;
  message: string;
}

export interface ToastStore extends ToastApi {
  items: Accessor<Toast[]>;
  dismiss(id: number): void;
}

export function createToastStore(timeoutMs = 5000): ToastStore {
  const [items, setItems] = createSignal<Toast[]>([]);
  let next = 1;
  const dismiss = (id: number) => setItems((list) => list.filter((t) => t.id !== id));
  const show = (kind: ToastKind, message: string) => {
    const id = next++;
    setItems((list) => [...list, { id, kind, message }]);
    if (timeoutMs > 0) setTimeout(() => dismiss(id), timeoutMs);
  };
  return {
    items,
    dismiss,
    show,
    success: (m) => show("success", m),
    error: (m) => show("error", m),
    info: (m) => show("info", m),
  };
}
