import { defineExtension } from "@edelweiss/sdk";
import { sessionInfo } from "@edelweiss/sdk/testing";
import { createMemoryHistory, MemoryRouter } from "@solidjs/router";
import { cleanup, render, waitFor } from "@solidjs/testing-library";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { createShell, ShellProviders } from "./context";
import { ShellRoutes } from "./routes";
import { fakeFetch, json } from "./test/fake-fetch";
import { ext } from "./test/fixtures";

// jsdom does not implement window.scrollTo, which the router calls from a timer after every navigation. The stub
// stays for the whole file: a timer that fires after the last test must not reach the "not implemented" stub.
beforeAll(() => {
  vi.spyOn(window, "scrollTo").mockImplementation(() => {});
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function setup(path: string, authenticated: boolean, extensions = [ext("system", "system", 10)]) {
  const served = {
    extensions,
    session: () =>
      authenticated
        ? json(200, sessionInfo())
        : json(401, { error: { code: "unauthenticated", message: "" } }),
  };
  const fetch = fakeFetch({
    "GET /api/session": () => served.session(),
    "GET /api/extensions": () => json(200, { extensions: served.extensions }),
    "POST /api/auth/login": () =>
      json(200, sessionInfo({ user: { name: "bob", uid: 1001, groups: ["bob"] } })),
  });
  vi.stubGlobal("fetch", fetch);
  const shell = createShell({ fetch, locale: "en", theme: "system" });
  void shell.session.load();
  const history = createMemoryHistory();
  history.set({ value: path });
  const loader = async () => ({ default: defineExtension({ component: () => <p>system ui</p> }) });
  const view = render(() => (
    <ShellProviders shell={shell}>
      <MemoryRouter history={history}>
        <ShellRoutes loader={loader} />
      </MemoryRouter>
    </ShellProviders>
  ));
  return { ...view, history, shell, fetch, served };
}

const extensionListRequests = (fetch: ReturnType<typeof fakeFetch>) =>
  fetch.calls.filter((call) => call.url === "/api/extensions").length;
const sessionRequests = (fetch: ReturnType<typeof fakeFetch>) =>
  fetch.calls.filter((call) => call.url === "/api/session").length;

describe("shell routes", () => {
  it("sends anonymous users to the login page with a return path", async () => {
    const { history } = setup("/x/system", false);
    await waitFor(() => expect(history.get()).toBe("/login?next=%2Fx%2Fsystem"));
  });

  it("opens the first extension from the start page and renders it in the layout", async () => {
    const { history, findByText, getByRole } = setup("/", true);
    await waitFor(() => expect(history.get()).toBe("/x/system"));
    expect(await findByText("system ui")).toBeTruthy();
    expect(getByRole("link", { name: /system/ })).toBeTruthy();
  });

  it("falls back to the extension settings when nothing is installed", async () => {
    const { history, findByText } = setup("/", true, []);
    await waitFor(() => expect(history.get()).toBe("/settings/extensions"));
    expect(await findByText("No extensions are installed yet.")).toBeTruthy();
  });

  it("shows the not-found page inside the layout for unknown paths", async () => {
    const { findByText, getByRole } = setup("/no/such/page", true);
    expect(await findByText("Page not found")).toBeTruthy();
    expect(getByRole("link", { name: "Back to the start page" }).getAttribute("href")).toBe("/");
    expect(getByRole("banner")).toBeTruthy();
  });

  it("forgets the extension list when the session ends, so the next user does not see it", async () => {
    const { shell, history, findByText, served } = setup("/x/system", true);
    await findByText("system ui");
    expect(shell.extensions.list()).toHaveLength(1);

    shell.session.unauthorized();
    await waitFor(() => expect(history.get()).toBe("/login?next=%2Fx%2Fsystem"));
    expect(shell.extensions.list()).toEqual([]);
    expect(shell.extensions.loaded()).toBe(false);

    // somebody else signs in on the same page and gets their own list
    served.extensions = [ext("other", "tools", 1)];
    await shell.session.login("bob", "pw");
    await waitFor(() => expect(shell.extensions.list().map((e) => e.id)).toEqual(["other"]));
    // back on the page they came from, where the first user's extension does not exist for them
    expect(await findByText("This extension is not installed.")).toBeTruthy();
    expect(history.get()).toBe("/x/system");
  });

  it("refreshes the extension list when the window regains focus, and stops when the area is left", async () => {
    const { findByText, fetch, unmount } = setup("/x/system", true);
    await findByText("system ui");
    const before = extensionListRequests(fetch);
    expect(before).toBeGreaterThan(0);

    window.dispatchEvent(new Event("focus"));
    expect(extensionListRequests(fetch)).toBe(before + 1);

    unmount();
    window.dispatchEvent(new Event("focus"));
    expect(extensionListRequests(fetch)).toBe(before + 1);
  });

  it("reloads the session when the window regains focus, so the navbar shows a changed hostname", async () => {
    const { findByText, fetch, served, unmount } = setup("/x/system", true);
    expect(await findByText("alpine")).toBeTruthy();
    const before = sessionRequests(fetch);
    expect(before).toBeGreaterThan(0);

    served.session = () => json(200, sessionInfo({ hostname: "renamed" }));
    window.dispatchEvent(new Event("focus"));
    expect(sessionRequests(fetch)).toBe(before + 1);
    expect(await findByText("renamed")).toBeTruthy();

    unmount();
    window.dispatchEvent(new Event("focus"));
    expect(sessionRequests(fetch)).toBe(before + 1);
  });

  it("ignores a failing session reload on focus and stays signed in", async () => {
    const { findByText, fetch, served, shell } = setup("/x/system", true);
    await findByText("system ui");
    const before = sessionRequests(fetch);
    const unhandled = vi.fn();
    process.on("unhandledRejection", unhandled);
    try {
      served.session = () => json(500, { error: { code: "internal", message: "boom" } });
      window.dispatchEvent(new Event("focus"));
      await waitFor(() => expect(sessionRequests(fetch)).toBe(before + 1));
      await new Promise((resolve) => setTimeout(resolve, 10)); // an unhandled rejection is reported after a tick
      expect(unhandled).not.toHaveBeenCalled();
    } finally {
      process.off("unhandledRejection", unhandled);
    }
    expect(shell.session.state()).toBe("authenticated");
    expect(await findByText("system ui")).toBeTruthy();
  });
});
