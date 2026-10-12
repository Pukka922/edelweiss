import { createMemoryHistory, MemoryRouter, Route } from "@solidjs/router";
import { render } from "@solidjs/testing-library";
import { createShell, type Shell, ShellProviders } from "../context";
import { fakeFetch, type Handler } from "./fake-fetch";

function renderIn(shell: Shell, ui: () => unknown, path: string) {
  const history = createMemoryHistory();
  history.set({ value: path });
  const view = render(() => (
    <ShellProviders shell={shell}>
      <MemoryRouter history={history}>
        <Route path="*" component={() => ui() as never} />
      </MemoryRouter>
    </ShellProviders>
  ));
  return { ...view, history };
}

/** Renders ui inside the shell providers and a memory router; fetch is a fake answering routes. */
export function mount(routes: Record<string, Handler>, ui: () => unknown, path = "/") {
  const fetch = fakeFetch(routes);
  const shell = createShell({ fetch, locale: "en", theme: "system" });
  return { ...renderIn(shell, ui, path), shell, fetch };
}

/** Takes a mounted page away and renders ui again in the same shell, as when the user leaves the page and comes back. */
export function remount(view: ReturnType<typeof mount>, ui: () => unknown, path = "/") {
  view.unmount();
  return { ...renderIn(view.shell, ui, path), shell: view.shell, fetch: view.fetch };
}

/** Lets pending fetches and promise callbacks settle. */
export const settle = () => new Promise((resolve) => setTimeout(resolve, 0));
