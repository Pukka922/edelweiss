import type { ToastKind } from "@edelweiss/sdk";
import { For } from "solid-js";
import { useShell } from "../context";

const alertClass: Record<ToastKind, string> = {
  success: "alert alert-success",
  error: "alert alert-error",
  info: "alert alert-info",
};

export function Toasts() {
  const shell = useShell();
  return (
    <div class="toast toast-end z-50">
      <For each={shell.toasts.items()}>
        {(toast) => (
          <div role="status" class={alertClass[toast.kind]}>
            <span>{toast.message}</span>
            <button
              type="button"
              class="btn btn-ghost btn-xs"
              aria-label="✕"
              onClick={() => shell.toasts.dismiss(toast.id)}
            >
              ✕
            </button>
          </div>
        )}
      </For>
    </div>
  );
}
