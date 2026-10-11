import { sessionInfo } from "@edelweiss/sdk/testing";
import { createMemoryHistory, MemoryRouter, Route } from "@solidjs/router";
import { cleanup, fireEvent, render, waitFor } from "@solidjs/testing-library";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { createShell, ShellProviders } from "../context";
import { fakeFetch, json } from "../test/fake-fetch";
import { Login, safeNext } from "./Login";

// jsdom does not implement window.scrollTo, which the router calls from a timer after every navigation. The stub
// stays for the whole file: a timer that fires after the last test must not reach the "not implemented" stub.
beforeAll(() => {
  vi.spyOn(window, "scrollTo").mockImplementation(() => {});
});

afterEach(() => {
  cleanup();
});

function setup(loginHandler: () => Response, search = "next=%2Fx%2Fsystem") {
  const fetch = fakeFetch({ "POST /api/auth/login": loginHandler });
  const shell = createShell({ fetch, locale: "en" });
  const history = createMemoryHistory();
  history.set({ value: `/login?${search}` });
  const view = render(() => (
    <ShellProviders shell={shell}>
      <MemoryRouter history={history}>
        <Route path="/login" component={Login} />
        <Route path="*" component={() => <p>elsewhere</p>} />
      </MemoryRouter>
    </ShellProviders>
  ));
  return { ...view, history, fetch, shell };
}

function fill(view: ReturnType<typeof setup>, user: string, password: string) {
  fireEvent.input(view.getByLabelText("User name"), { target: { value: user } });
  fireEvent.input(view.getByLabelText("Password"), { target: { value: password } });
  fireEvent.submit(view.getByRole("button", { name: "Sign in" }).closest("form")!);
}

describe("Login", () => {
  it("signs in and returns to the requested page", async () => {
    const view = setup(() => json(200, sessionInfo()));
    fill(view, "alice", "secret");
    await waitFor(() => expect(view.history.get()).toBe("/x/system"));
    expect(view.fetch.calls[0]!.body).toEqual({ username: "alice", password: "secret" });
  });

  it("shows a clear message for wrong credentials and rate limits", async () => {
    const view = setup(() => json(401, { error: { code: "invalid_credentials", message: "x" } }));
    fill(view, "alice", "wrong");
    expect((await view.findByRole("alert")).textContent).toBe("Invalid user name or password.");
    // the form stays usable and does not keep the rejected password
    expect((view.getByLabelText("Password") as HTMLInputElement).value).toBe("");
    expect((view.getByRole("button", { name: "Sign in" }) as HTMLButtonElement).disabled).toBe(false);
  });

  it("explains rate limiting", async () => {
    const view = setup(() => json(429, { error: { code: "too_many_requests", message: "slow down" } }));
    fill(view, "alice", "secret");
    expect((await view.findByRole("alert")).textContent).toBe(
      "Too many failed attempts. Please wait a few minutes.",
    );
    expect(view.history.get()).toBe("/login?next=%2Fx%2Fsystem");
  });

  it("tells the user when the session has expired", async () => {
    const view = setup(() => json(200, sessionInfo()));
    await view.shell.session.login("alice", "secret");
    view.shell.session.unauthorized();
    view.history.set({ value: "/login" });
    expect(await view.findByText("Your session has expired. Please sign in again.")).toBeTruthy();
  });

  it("keeps the query and the fragment of the page it returns to", async () => {
    const view = setup(() => json(200, sessionInfo()), "next=%2Fx%2Fsystem%2Fdisks%3Ftab%3D2%23top");
    fill(view, "alice", "secret");
    await waitFor(() => expect(view.history.get()).toBe("/x/system/disks?tab=2#top"));
  });

  it.each([
    ["a tab in it", "%2F%09%2Fevil.example"],
    ["a line feed in it", "%2F%0A%2Fevil.example"],
    ["a carriage return in it", "%2F%0D%2Fevil.example"],
    ["a backslash in it", "%2F%5Cevil.example"],
    ["a dot segment: /.//evil.example", "%2F.%2F%2Fevil.example"],
    ["a parent segment: /x/..//evil.example", "%2Fx%2F..%2F%2Fevil.example"],
    ["an encoded dot segment: /%2e//evil.example", "%2F%252e%2F%2Fevil.example"],
    ["encoded parent segments: /%2E%2E//evil.example/path", "%2F%252E%252E%2F%2Fevil.example%2Fpath"],
  ])("does not follow a return path with %s", async (_name, next) => {
    // The router hands over the decoded value ("/\t/evil.example"), which the URL parser reads as "//evil.example".
    const view = setup(() => json(200, sessionInfo()), `next=${next}`);
    fill(view, "alice", "secret");
    await waitFor(() => expect(view.history.get()).toBe("/"));
  });

  it("safeNext only allows local paths", () => {
    expect(safeNext("/x/system")).toBe("/x/system");
    expect(safeNext("/x/system/disks?tab=2#top")).toBe("/x/system/disks?tab=2#top");
    expect(safeNext("//evil.example")).toBe("/");
    expect(safeNext("/\\evil.example")).toBe("/");
    expect(safeNext("https://evil.example")).toBe("/");
    expect(safeNext("evil.example")).toBe("/");
    expect(safeNext("/login")).toBe("/");
    expect(safeNext("/login?next=/x")).toBe("/");
    expect(safeNext("/x/../login")).toBe("/");
    expect(safeNext(undefined)).toBe("/");
    // ?next=a&next=b arrives as an array
    expect(safeNext(["/a", "/b"] as unknown as string)).toBe("/");
  });

  it("safeNext rejects control characters, which the URL parser strips into another host", () => {
    // sanity check of the premise: the browser really resolves this to another host
    expect(new URL("/\t/evil.example", window.location.origin).host).toBe("evil.example");
    for (const next of ["/\t/evil.example", "/\n/evil.example", "/\r/evil.example", "/\u0000x", "/x\u007f"]) {
      expect(safeNext(next), JSON.stringify(next)).toBe("/");
    }
  });

  it("safeNext rejects dot segments that normalise to a protocol-relative path", () => {
    // sanity check of the premise: same origin, but the normalised path starts with "//"
    const parsed = new URL("/.//evil.example", window.location.origin);
    expect(parsed.origin).toBe(window.location.origin);
    expect(parsed.pathname).toBe("//evil.example");
    for (const next of [
      "/.//evil.example",
      "/x/..//evil.example",
      "/%2e//evil.example",
      "/%2E%2E//evil.example/path",
    ]) {
      expect(safeNext(next), next).toBe("/");
    }
    // dot segments that stay local are still fine, normalised
    expect(safeNext("/x/./y/../z?a=1")).toBe("/x/z?a=1");
  });
});
