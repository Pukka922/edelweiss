import { createMemoryHistory, MemoryRouter, Route } from "@solidjs/router";
import { render } from "@solidjs/testing-library";
import { createShell, ShellProviders } from "../context";
import { fakeFetch, type Handler } from "./fake-fetch";

/** Renders ui inside the shell providers and a memory router; fetch is a fake answering routes. */
export function mount(routes: Record<string, Handler>, ui: () => unknown, path = "/") {
  const fetch = fakeFetch(routes);
  const shell = createShell({ fetch, locale: "en", theme: "system" });
  const history = createMemoryHistory();
  history.set({ value: path });
  const view = render(() => (
    <ShellProviders shell={shell}>
      <MemoryRouter history={history}>
        <Route path="*" component={() => ui() as never} />
      </MemoryRouter>
    </ShellProviders>
  ));
  return { ...view, shell, fetch, history };
}

/** Lets pending fetches and promise callbacks settle. */
export const settle = () => new Promise((resolve) => setTimeout(resolve, 0));
