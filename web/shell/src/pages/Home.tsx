import { LoadingState } from "@edelweiss/sdk";
import { Navigate } from "@solidjs/router";
import { Show } from "solid-js";
import { useShell } from "../context";

/** Start page: the first extension with a UI, or the extension settings. */
export function Home() {
  const shell = useShell();
  const target = () => {
    const first = shell.extensions.list().find((e) => e.ui);
    return first ? `/x/${first.id}` : "/settings/extensions";
  };
  return (
    <Show when={shell.extensions.loaded()} fallback={<LoadingState />}>
      <Navigate href={target()} />
    </Show>
  );
}
