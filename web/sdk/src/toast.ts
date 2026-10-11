import { createContext, useContext } from "solid-js";

export type ToastKind = "success" | "error" | "info";

export interface ToastApi {
  show(kind: ToastKind, message: string): void;
  success(message: string): void;
  error(message: string): void;
  info(message: string): void;
}

export const ToastContext = createContext<ToastApi>();

export function useToast(): ToastApi {
  const toasts = useContext(ToastContext);
  if (!toasts) throw new Error("useToast must be used inside the edelweiss shell");
  return toasts;
}
