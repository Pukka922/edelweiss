import { LoadingState } from "@edelweiss/sdk";
import { Navigate, Route, type RouteSectionProps, useLocation } from "@solidjs/router";
import { createEffect, Match, onCleanup, Switch } from "solid-js";
import { AdminDialog } from "./components/AdminDialog";
import { Layout } from "./components/Layout";
import { useShell } from "./context";
import { ExtensionHost, type ModuleLoader } from "./pages/ExtensionHost";
import { ExtensionsPage } from "./pages/Extensions";
import { Home } from "./pages/Home";
import { Login } from "./pages/Login";
import { NotFound } from "./pages/NotFound";

/** Signed-in area: redirects to the login page and keeps the extension list and the session data fresh. */
function Authenticated(props: RouteSectionProps) {
  const shell = useShell();
  const location = useLocation();
  createEffect(() => {
    const state = shell.session.state();
    if (state === "authenticated") void shell.extensions.reload();
    // The list belongs to the session: after a sign-out or an expiry the next user must not see it, not even briefly.
    else if (state === "anonymous") shell.extensions.clear();
  });
  const onFocus = () => {
    if (shell.session.state() !== "authenticated") return;
    void shell.extensions.reload();
    // Session data (the hostname in the navbar, the admin flag) changes behind the shell's back, e.g. in another tab.
    // A failed reload is not worth reporting: the next focus tries again, and a 401 is already handled by the store.
    shell.session.refresh().catch(() => {});
  };
  window.addEventListener("focus", onFocus);
  onCleanup(() => window.removeEventListener("focus", onFocus));
  return (
    <Switch>
      <Match when={shell.session.state() === "loading"}>
        <LoadingState />
      </Match>
      <Match when={shell.session.state() === "anonymous"}>
        <Navigate href={`/login?next=${encodeURIComponent(location.pathname + location.search)}`} />
      </Match>
      <Match when={shell.session.state() === "authenticated"}>
        <Layout>{props.children}</Layout>
        <AdminDialog />
      </Match>
    </Switch>
  );
}

export function ShellRoutes(props: { loader?: ModuleLoader }) {
  return (
    <>
      <Route path="/login" component={Login} />
      <Route path="/" component={Authenticated}>
        <Route path="/" component={Home} />
        <Route path="/x/:ext/*rest" component={() => <ExtensionHost loader={props.loader} />} />
        <Route path="/settings/extensions" component={ExtensionsPage} />
        <Route path="*404" component={NotFound} />
      </Route>
    </>
  );
}
