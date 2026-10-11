import { useLocation, useNavigate } from "@solidjs/router";
import { type Component, createMemo, type JSX, Show } from "solid-js";
import { Dynamic } from "solid-js/web";
import { useExtension } from "./extension";

export interface ExtensionRoute {
  /** Path inside the extension, e.g. "/" or "/disks/:id". */
  path: string;
  component: Component<{ params: Record<string, string> }>;
}

export interface RouteMatch {
  route: ExtensionRoute;
  params: Record<string, string>;
}

const segments = (path: string) => path.split("/").filter(Boolean);

/** First route whose segments match path; ":name" segments become params. */
export function matchRoute(routes: readonly ExtensionRoute[], path: string): RouteMatch | undefined {
  const parts = segments(path);
  for (const route of routes) {
    const pattern = segments(route.path);
    if (pattern.length !== parts.length) continue;
    const params: Record<string, string> = {};
    let matched = true;
    for (let i = 0; i < pattern.length && matched; i++) {
      const want = pattern[i]!;
      const got = parts[i]!;
      if (want.startsWith(":")) {
        try {
          params[want.slice(1)] = decodeURIComponent(got);
        } catch {
          matched = false;
        }
      } else if (want !== got) {
        matched = false;
      }
    }
    if (matched) return { route, params };
  }
  return undefined;
}

/** Path inside the extension for a shell pathname ("/x/system/disks/1" → "/disks/1"). */
export function extensionPath(basePath: string, pathname: string): string {
  if (pathname.startsWith(`${basePath}/`)) return pathname.slice(basePath.length);
  return "/";
}

/** Renders the extension route matching the current location. */
export function ExtensionRouter(props: { routes: ExtensionRoute[]; fallback?: JSX.Element }): JSX.Element {
  const ext = useExtension();
  const location = useLocation();
  const match = createMemo(() => matchRoute(props.routes, extensionPath(ext.basePath, location.pathname)));
  return (
    <Show when={match()} fallback={props.fallback}>
      {(m) => <Dynamic component={m().route.component} params={m().params} />}
    </Show>
  );
}

/** Navigates to a path inside the current extension. */
export function useExtensionNavigate(): (to: string, options?: { replace?: boolean }) => void {
  const ext = useExtension();
  const navigate = useNavigate();
  return (to, options) => navigate(`${ext.basePath}${to.startsWith("/") ? to : `/${to}`}`, options);
}
