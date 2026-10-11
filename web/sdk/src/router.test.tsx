import { createMemoryHistory, MemoryRouter, Route } from "@solidjs/router";
import { fireEvent, render } from "@solidjs/testing-library";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ExtensionContext, extensionContextFor } from "./extension";
import { ExtensionRouter, extensionPath, matchRoute, useExtensionNavigate } from "./router";

const routes = [
  { path: "/", component: () => <p>home</p> },
  { path: "/disks/:id", component: (p: { params: Record<string, string> }) => <p>disk {p.params.id}</p> },
];

describe("matchRoute", () => {
  it("matches static and parameter segments", () => {
    expect(matchRoute(routes, "/")?.route).toBe(routes[0]);
    expect(matchRoute(routes, "/disks/sd%20a")?.params).toEqual({ id: "sd a" });
    expect(matchRoute(routes, "/disks")).toBeUndefined();
    expect(matchRoute(routes, "/disks/%E0%A4%A")).toBeUndefined();
  });

  it("extensionPath strips the base path", () => {
    expect(extensionPath("/x/system", "/x/system")).toBe("/");
    expect(extensionPath("/x/system", "/x/system/disks/1")).toBe("/disks/1");
    expect(extensionPath("/x/system", "/x/systemd")).toBe("/");
  });
});

describe("ExtensionRouter", () => {
  // jsdom does not implement scrolling; the router scrolls to the top in a timer after navigating.
  beforeEach(() => {
    vi.spyOn(window, "scrollTo").mockImplementation(() => {});
  });
  afterEach(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0));
    vi.restoreAllMocks();
  });

  it("renders the matching route and navigates relative to the extension", async () => {
    function Nav() {
      const go = useExtensionNavigate();
      return (
        <button type="button" onClick={() => go("/disks/7")}>
          go
        </button>
      );
    }
    const history = createMemoryHistory();
    history.set({ value: "/x/system" });
    const { findByText, getByRole } = render(() => (
      <MemoryRouter history={history}>
        <Route
          path="/x/:ext/*rest"
          component={() => (
            <ExtensionContext.Provider value={extensionContextFor("system")}>
              <Nav />
              <ExtensionRouter routes={routes} fallback={<p>missing</p>} />
            </ExtensionContext.Provider>
          )}
        />
      </MemoryRouter>
    ));
    expect(await findByText("home")).toBeTruthy();
    fireEvent.click(getByRole("button"));
    expect(await findByText("disk 7")).toBeTruthy();
    expect(history.get()).toBe("/x/system/disks/7");
  });
});
