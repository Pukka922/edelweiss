import { defineExtension } from "@edelweiss/sdk";
import { sessionInfo } from "@edelweiss/sdk/testing";
import { cleanup, fireEvent, render, waitFor } from "@solidjs/testing-library";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { App } from "./App";
import { createShell } from "./context";
import { fakeFetch, type Handler, json } from "./test/fake-fetch";
import { ext } from "./test/fixtures";

// jsdom does not implement window.scrollTo, which the router calls from a timer after every navigation. The stub
// stays for the whole file: a timer that fires after the last test must not reach the "not implemented" stub.
beforeAll(() => {
  vi.spyOn(window, "scrollTo").mockImplementation(() => {});
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.history.replaceState(null, "", "/");
});

function start(authenticated: boolean, logout: Handler = () => new Response(null, { status: 204 })) {
  const fetch = fakeFetch({
    "POST /api/auth/logout": logout,
    "GET /api/session": () =>
      authenticated
        ? json(200, sessionInfo())
        : json(401, { error: { code: "unauthenticated", message: "" } }),
    "GET /api/extensions": () => json(200, { extensions: [ext("app-demo", "system", 1, "Demo")] }),
  });
  vi.stubGlobal("fetch", fetch);
  const shell = createShell({ fetch, locale: "en", theme: "system" });
  const loader = async () => ({ default: defineExtension({ component: () => <p>demo ui</p> }) });
  const view = render(() => <App shell={shell} loader={loader} />);
  return { ...view, shell, fetch };
}

describe("App", () => {
  it("loads the session on start and shows the login page when nobody is signed in", async () => {
    const { findByRole } = start(false);
    expect(await findByRole("heading", { name: "Sign in" })).toBeTruthy();
    expect(window.location.pathname).toBe("/login");
    expect(window.location.search).toBe("?next=%2F");
  });

  it("opens the first extension in the layout and shows toasts above every page", async () => {
    const { findByText, shell } = start(true);
    expect(await findByText("demo ui")).toBeTruthy();
    expect(window.location.pathname).toBe("/x/app-demo");
    expect(await findByText("alpine")).toBeTruthy();

    shell.toasts.info("Hello from a toast");
    await waitFor(() => expect(document.body.textContent).toContain("Hello from a toast"));
  });

  // Signing out flips the session to anonymous, which makes the route guard redirect to /login?next=... by itself;
  // the user menu then navigates to a plain /login. Whatever the server answers, that has to be where we end up.
  it.each([
    ["succeeds", () => new Response(null, { status: 204 }), false],
    ["fails", () => json(500, { error: { code: "internal", message: "boom" } }), true],
  ] as [string, Handler, boolean][])(
    "signing out ends on the plain login page when the request %s",
    async (_name, logout, warned) => {
      const { findByText, findByRole, getByRole, shell } = start(true, logout);
      await findByText("demo ui");
      fireEvent.click(getByRole("button", { name: "Sign out" }));
      expect(await findByRole("heading", { name: "Sign in" })).toBeTruthy();
      await new Promise((resolve) => setTimeout(resolve, 20)); // a late redirect from the guard would show up here
      expect(window.location.pathname).toBe("/login");
      expect(window.location.search).toBe("");
      expect(shell.session.state()).toBe("anonymous");
      expect(shell.extensions.list()).toEqual([]);
      expect(document.body.textContent?.includes("Sign-out could not be confirmed")).toBe(warned);
    },
  );
});
