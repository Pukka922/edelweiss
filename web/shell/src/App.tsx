import { Router } from "@solidjs/router";
import { onMount } from "solid-js";
import { Toasts } from "./components/Toasts";
import { createShell, type Shell, ShellProviders } from "./context";
import type { ModuleLoader } from "./pages/ExtensionHost";
import { ShellRoutes } from "./routes";

export function App(props: { shell?: Shell; loader?: ModuleLoader }) {
  const shell = props.shell ?? createShell();
  onMount(() => {
    void shell.session.load().catch(() => {});
  });
  return (
    <ShellProviders shell={shell}>
      <Router
        root={(p) => (
          <>
            {p.children}
            <Toasts />
          </>
        )}
      >
        <ShellRoutes loader={props.loader} />
      </Router>
    </ShellProviders>
  );
}
