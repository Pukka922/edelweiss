import { sessionInfo } from "@edelweiss/sdk/testing";
import { cleanup, fireEvent, waitFor } from "@solidjs/testing-library";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { ExtensionsPage } from "../pages/Extensions";
import { type Handler, json } from "../test/fake-fetch";
import { ext, noUpdates } from "../test/fixtures";
import { mount, settle } from "../test/mount";
import { AdminDialog } from "./AdminDialog";
import { LanguageSelect, Navbar, ThemeSelect } from "./Navbar";
import { Sidebar } from "./Sidebar";
import { Toasts } from "./Toasts";

// jsdom does not implement window.scrollTo, which the router calls from a timer after every navigation. The stub
// stays for the whole file: a timer that fires after the last test must not reach the "not implemented" stub.
beforeAll(() => {
  vi.spyOn(window, "scrollTo").mockImplementation(() => {});
});

afterEach(() => {
  cleanup();
  localStorage.clear();
  delete document.documentElement.dataset.theme;
  document.documentElement.removeAttribute("lang");
  // biome-ignore lint/suspicious/noDocumentCookie: removes the cookie applyLocale() wrote
  document.cookie = "edelweiss_lang=; Path=/; Max-Age=0";
});

describe("shell ui", () => {
  it("sidebar groups extensions by section", async () => {
    const view = mount(
      {
        "GET /api/extensions": () =>
          json(200, { extensions: [ext("tool", "tools", 1, "Tool"), ext("sys", "system", 1, "Sys")] }),
      },
      () => <Sidebar />,
    );
    await view.shell.extensions.reload();
    const headings = view.getAllByRole("heading").map((h) => h.textContent);
    expect(headings).toEqual(["System", "Tools", "Settings"]);
    expect(view.getByRole("link", { name: "Sys" }).getAttribute("href")).toBe("/x/sys");
  });

  it("admin toggle opens the dialog and enables admin mode", async () => {
    const view = mount(
      {
        "GET /api/session": () => json(200, sessionInfo({ canAdmin: true })),
        "POST /api/session/admin": () => json(200, sessionInfo({ admin: true })),
      },
      () => (
        <>
          <Navbar />
          <AdminDialog />
        </>
      ),
    );
    await view.shell.session.load();
    fireEvent.click(view.getByRole("button", { name: "Admin mode" }));
    const dialog = await view.findByRole("dialog", { name: "Administrative access" });
    fireEvent.input(view.getByLabelText("Password"), { target: { value: "pw" } });
    fireEvent.submit(dialog.querySelector("form")!);
    await waitFor(() => expect(view.shell.session.admin()).toBe(true));
    expect(view.queryByRole("dialog")).toBeNull();
    expect(view.getByRole("button", { name: "Admin" }).getAttribute("aria-pressed")).toBe("true");
  });

  it("removes an extension after confirmation in admin mode", async () => {
    let list = [ext("demo", "tools", 1, "Demo")];
    const view = mount(
      {
        "GET /api/session": () => json(200, sessionInfo({ admin: true })),
        "GET /api/extensions/updates": noUpdates,
        "GET /api/extensions": () => json(200, { extensions: list }),
        "DELETE /api/extensions/demo": () => {
          list = [];
          return new Response(null, { status: 204 });
        },
      },
      () => <ExtensionsPage />,
    );
    await view.shell.session.load();
    await view.shell.extensions.reload();
    fireEvent.click(view.getByRole("button", { name: "Remove" }));
    const dialog = await view.findByRole("dialog", { name: "Remove extension" });
    fireEvent.click(dialog.querySelector(".btn-error")!);
    // the toast follows the list update by a few microtasks: wait for the last thing that happens
    await waitFor(() => expect(view.shell.toasts.items()[0]?.message).toBe("Demo was removed."));
    expect(view.shell.extensions.list()).toEqual([]);
  });
});

describe("admin dialog", () => {
  const adminSession = { "GET /api/session": () => json(200, sessionInfo({ canAdmin: true })) };

  async function open(routes: Record<string, Handler> = {}) {
    const view = mount({ ...adminSession, ...routes }, () => <AdminDialog />);
    await view.shell.session.load();
    const pending = view.shell.session.requestAdmin();
    const dialog = await view.findByRole("dialog", { name: "Administrative access" });
    return { ...view, pending, dialog };
  }

  type Opened = Awaited<ReturnType<typeof open>>;
  const waysToClose: [string, (v: Opened) => void][] = [
    ["the cancel button", (v) => fireEvent.click(v.getByRole("button", { name: "Cancel" }))],
    ["the escape key", () => fireEvent.keyDown(document, { key: "Escape" })],
    ["a click on the backdrop", (v) => fireEvent.click(v.container.querySelector(".modal-backdrop")!)],
  ];

  // A cancelled dialog must answer the pending request, or whoever awaits requestAdmin() hangs forever.
  it.each(waysToClose)("answers a pending request with false when closed with %s", async (_name, close) => {
    const view = await open();
    expect(view.shell.session.adminRequested()).toBe(true);
    close(view);
    expect(await view.pending).toBe(false);
    expect(view.shell.session.adminRequested()).toBe(false);
    expect(view.queryByRole("dialog")).toBeNull();
    expect(view.fetch.calls.filter((c) => c.url === "/api/session/admin")).toEqual([]);
  });

  it("ignores the escape key while it is closed", async () => {
    const view = mount(adminSession, () => <AdminDialog />);
    await view.shell.session.load();
    const spy = vi.spyOn(view.shell.session, "answerAdminRequest");
    fireEvent.keyDown(document, { key: "Escape" });
    expect(spy).not.toHaveBeenCalled();
  });

  it("does not keep a rejected password", async () => {
    const view = await open({
      "POST /api/session/admin": () => json(401, { error: { code: "invalid_credentials", message: "x" } }),
    });
    fireEvent.input(view.getByLabelText("Password"), { target: { value: "wrong" } });
    fireEvent.submit(view.dialog.querySelector("form")!);
    await view.findByRole("alert");
    expect((view.getByLabelText("Password") as HTMLInputElement).value).toBe("");
    view.shell.session.answerAdminRequest(false);
  });

  // The API client has no timeout, so a request that hangs must not trap the user in the dialog. Cancelling answers
  // the pending request with false at once; if the password check succeeds after all, admin mode is switched off
  // again, so it does not turn on against the user's explicit cancel.
  describe("while the password is being checked", () => {
    const calls = (view: Opened, method: string) =>
      view.fetch.calls.filter((c) => c.method === method && c.url === "/api/session/admin");

    /** A dialog whose password check hangs until check() is called with the server's verdict. */
    async function hanging(switchOff: Handler = () => json(200, sessionInfo({ admin: false }))) {
      let check = (_verdict: Response) => {};
      const verdict = new Promise<Response>((resolve) => {
        check = resolve;
      });
      const view = await open({
        "POST /api/session/admin": () => verdict,
        "DELETE /api/session/admin": switchOff,
      });
      fireEvent.input(view.getByLabelText("Password"), { target: { value: "pw" } });
      fireEvent.submit(view.dialog.querySelector("form")!);
      await waitFor(() => expect(calls(view, "POST")).toHaveLength(1));
      return { view, check };
    }

    it.each(waysToClose)(
      "can be left with %s and then switches admin mode off again",
      async (_name, close) => {
        const { view, check } = await hanging();
        close(view);
        expect(await view.pending).toBe(false);
        expect(view.shell.session.adminRequested()).toBe(false);
        expect(view.queryByRole("dialog")).toBeNull();
        expect(calls(view, "DELETE")).toHaveLength(0);

        check(json(200, sessionInfo({ admin: true }))); // the server accepted the password after all
        await waitFor(() => expect(calls(view, "DELETE")).toHaveLength(1));
        await waitFor(() => expect(view.shell.session.admin()).toBe(false));
      },
    );

    it("does nothing more when the cancelled check fails", async () => {
      const { view, check } = await hanging();
      fireEvent.keyDown(document, { key: "Escape" });
      expect(await view.pending).toBe(false);

      check(json(401, { error: { code: "invalid_credentials", message: "x" } }));
      await settle();
      expect(calls(view, "DELETE")).toHaveLength(0);
      expect(view.shell.session.admin()).toBe(false);
    });

    it("ignores a failing switch-off after a cancel", async () => {
      const unhandled = vi.fn();
      process.on("unhandledRejection", unhandled);
      try {
        const { view, check } = await hanging(() =>
          json(500, { error: { code: "internal", message: "boom" } }),
        );
        fireEvent.keyDown(document, { key: "Escape" });
        expect(await view.pending).toBe(false);

        check(json(200, sessionInfo({ admin: true })));
        await waitFor(() => expect(calls(view, "DELETE")).toHaveLength(1));
        await settle(); // unhandled rejections are reported after the microtask queue drained
        expect(unhandled).not.toHaveBeenCalled();
        expect(view.shell.toasts.items()).toEqual([]);
      } finally {
        process.off("unhandledRejection", unhandled);
      }
    });

    it("keeps admin mode when it was not cancelled", async () => {
      const { view, check } = await hanging();
      check(json(200, sessionInfo({ admin: true })));
      expect(await view.pending).toBe(true);
      expect(view.shell.session.admin()).toBe(true);
      expect(view.queryByRole("dialog")).toBeNull();
      await settle();
      expect(calls(view, "DELETE")).toHaveLength(0);
    });
  });

  it("can be cancelled again after a failed attempt", async () => {
    const view = await open({
      "POST /api/session/admin": () => json(401, { error: { code: "invalid_credentials", message: "x" } }),
    });
    fireEvent.input(view.getByLabelText("Password"), { target: { value: "wrong" } });
    fireEvent.submit(view.dialog.querySelector("form")!);
    await view.findByRole("alert");
    fireEvent.keyDown(document, { key: "Escape" });
    expect(await view.pending).toBe(false);
  });

  it("starts with a clean form every time it opens", async () => {
    const view = await open({
      "POST /api/session/admin": () => json(401, { error: { code: "invalid_credentials", message: "x" } }),
    });
    fireEvent.input(view.getByLabelText("Password"), { target: { value: "wrong" } });
    fireEvent.submit(view.dialog.querySelector("form")!);
    expect((await view.findByRole("alert")).textContent).toBe("Wrong password.");
    // still open and usable after the failure
    expect(view.shell.session.adminRequested()).toBe(true);

    fireEvent.click(view.getByRole("button", { name: "Cancel" }));
    expect(await view.pending).toBe(false);

    const again = view.shell.session.requestAdmin();
    await view.findByRole("dialog", { name: "Administrative access" });
    expect(view.queryByRole("alert")).toBeNull();
    expect((view.getByLabelText("Password") as HTMLInputElement).value).toBe("");
    view.shell.session.answerAdminRequest(false);
    expect(await again).toBe(false);
  });
});

describe("navbar", () => {
  const signedIn = { "GET /api/session": () => json(200, sessionInfo()) };

  // session.logout() clears the local session and then rethrows a failed request: the navbar must not leak that
  // as an unhandled rejection and still leaves for the login page. Unless the server confirmed that the session is
  // gone (204, or 401 for one that had already ended), it also says so: the session may still be active there.
  const notConfirmed = "Sign-out could not be confirmed by the server. Your session may still be active.";
  it.each([
    ["succeeds", () => new Response(null, { status: 204 }), []],
    ["fails", () => json(500, { error: { code: "internal", message: "boom" } }), [notConfirmed]],
    ["cannot reach the server", () => Promise.reject(new TypeError("Failed to fetch")), [notConfirmed]],
    [
      "is answered 401 because the session had ended already",
      () => json(401, { error: { code: "unauthenticated", message: "" } }),
      [],
    ],
  ] as [string, Handler, string[]][])(
    "signs out and lands on the login page when the request %s",
    async (_name, answer, toasts) => {
      const unhandled = vi.fn();
      process.on("unhandledRejection", unhandled);
      try {
        const view = mount({ ...signedIn, "POST /api/auth/logout": answer }, () => <Navbar />, "/x/system");
        await view.shell.session.load();
        fireEvent.click(view.getByRole("button", { name: "Sign out" }));
        await waitFor(() => expect(view.history.get()).toBe("/login"));
        expect(view.shell.session.state()).toBe("anonymous");
        expect(view.fetch.calls.some((c) => c.url === "/api/auth/logout")).toBe(true);
        expect(view.shell.toasts.items().map((t) => t.message)).toEqual(toasts);
        expect(view.shell.toasts.items().every((t) => t.kind === "error")).toBe(true);
        await settle(); // unhandled rejections are reported after the microtask queue drained
        expect(unhandled).not.toHaveBeenCalled();
      } finally {
        process.off("unhandledRejection", unhandled);
      }
    },
  );

  it("shows the host, the version and the signed-in user", async () => {
    const view = mount(signedIn, () => <Navbar />);
    await view.shell.session.load();
    expect(view.getByText("alpine")).toBeTruthy();
    expect(view.getByText("edelweiss 0.1.0")).toBeTruthy();
    expect(view.getByText("alice")).toBeTruthy();
  });

  // On a phone the navbar row is crowded; the selects must keep the width their text and chevron need, and the admin
  // toggle gives up its label (not for screen readers) instead.
  it("keeps the selects at their content width and the admin label readable for assistive technology", async () => {
    const view = mount(signedIn, () => <Navbar />);
    await view.shell.session.load();
    for (const name of ["Language", "Theme"]) {
      expect(view.getByRole("combobox", { name }).classList.contains("shrink-0")).toBe(true);
    }
    const label = view.getByRole("button", { name: "Admin mode" }).querySelector("span")!;
    expect(label.classList.contains("sr-only")).toBe(true);
    expect(label.classList.contains("sm:not-sr-only")).toBe(true);
  });

  it("hides the admin toggle from users who may not use admin mode", async () => {
    const view = mount({ "GET /api/session": () => json(200, sessionInfo({ canAdmin: false })) }, () => (
      <Navbar />
    ));
    await view.shell.session.load();
    expect(view.queryByRole("button", { name: "Admin mode" })).toBeNull();
  });

  it("leaves admin mode", async () => {
    const view = mount(
      {
        "GET /api/session": () => json(200, sessionInfo({ admin: true })),
        "DELETE /api/session/admin": () => json(200, sessionInfo({ admin: false })),
      },
      () => <Navbar />,
    );
    await view.shell.session.load();
    fireEvent.click(view.getByRole("button", { name: "Admin" }));
    await waitFor(() => expect(view.shell.session.admin()).toBe(false));
    expect(view.getByRole("button", { name: "Admin mode" }).getAttribute("aria-pressed")).toBe("false");
  });

  it("reports a failure to leave admin mode", async () => {
    const view = mount(
      {
        "GET /api/session": () => json(200, sessionInfo({ admin: true })),
        "DELETE /api/session/admin": () => json(500, { error: { code: "internal", message: "boom" } }),
      },
      () => <Navbar />,
    );
    await view.shell.session.load();
    fireEvent.click(view.getByRole("button", { name: "Admin" }));
    await waitFor(() =>
      expect(view.shell.toasts.items()[0]?.message).toBe("Could not leave admin mode: boom"),
    );
    expect(view.shell.session.admin()).toBe(true);
  });

  it("switches the language and remembers it", () => {
    const view = mount({}, () => <LanguageSelect />);
    fireEvent.change(view.getByLabelText("Language"), { target: { value: "de" } });
    expect(view.shell.i18n.locale()).toBe("de");
    expect(view.getByLabelText("Sprache")).toBeTruthy();
    expect(localStorage.getItem("edelweiss.lang")).toBe("de");
  });

  it("switches the theme and remembers it", () => {
    const view = mount({}, () => <ThemeSelect />);
    fireEvent.change(view.getByLabelText("Theme"), { target: { value: "dark" } });
    expect(view.shell.theme()).toBe("dark");
    expect(document.documentElement.dataset.theme).toBe("dark");
    expect(localStorage.getItem("edelweiss.theme")).toBe("dark");
    fireEvent.change(view.getByLabelText("Theme"), { target: { value: "system" } });
    expect(document.documentElement.dataset.theme).toBeUndefined();
    expect(localStorage.getItem("edelweiss.theme")).toBeNull();
  });
});

describe("extension settings", () => {
  // in admin mode the page checks for updates
  const admin = {
    "GET /api/session": () => json(200, sessionInfo({ admin: true })),
    "GET /api/extensions/updates": noUpdates,
  };

  it("lists installed extensions and marks development and admin-only ones", async () => {
    const view = mount(
      {
        ...admin,
        "GET /api/extensions": () =>
          json(200, {
            extensions: [{ ...ext("live", "tools", 1, "Live"), dev: true, requiresAdmin: true }],
          }),
      },
      () => <ExtensionsPage />,
    );
    await view.shell.extensions.reload();
    expect(view.getByText("Live")).toBeTruthy();
    expect(view.getByText("dev")).toBeTruthy();
    expect(view.getByText("admin only")).toBeTruthy();
    expect((view.getByRole("button", { name: "Remove" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("offers removal only to users who are in or may enter admin mode", async () => {
    const routes = {
      "GET /api/extensions": () => json(200, { extensions: [ext("demo", "tools", 1, "Demo")] }),
    };
    const removeButton = (view: ReturnType<typeof mount>) =>
      view.getByRole("button", { name: "Remove" }) as HTMLButtonElement;

    const plain = mount(
      { ...routes, "GET /api/session": () => json(200, sessionInfo({ canAdmin: false })) },
      () => <ExtensionsPage />,
    );
    await plain.shell.session.load();
    await plain.shell.extensions.reload();
    expect(removeButton(plain).disabled).toBe(true);
    cleanup();

    const mayAdmin = mount(
      { ...routes, "GET /api/session": () => json(200, sessionInfo({ canAdmin: true })) },
      () => <ExtensionsPage />,
    );
    await mayAdmin.shell.session.load();
    await mayAdmin.shell.extensions.reload();
    expect(removeButton(mayAdmin).disabled).toBe(false);
  });

  it("keeps the extension when the confirmation is cancelled", async () => {
    const view = mount(
      {
        ...admin,
        "GET /api/extensions": () => json(200, { extensions: [ext("demo", "tools", 1, "Demo")] }),
      },
      () => <ExtensionsPage />,
    );
    await view.shell.session.load();
    await view.shell.extensions.reload();
    fireEvent.click(view.getByRole("button", { name: "Remove" }));
    await view.findByRole("dialog", { name: "Remove extension" });
    fireEvent.click(view.getByRole("button", { name: "Cancel" }));
    expect(view.queryByRole("dialog")).toBeNull();
    expect(view.fetch.calls.some((c) => c.method === "DELETE")).toBe(false);
  });

  it("reports a failed removal and keeps the extension listed", async () => {
    const view = mount(
      {
        ...admin,
        "GET /api/extensions": () => json(200, { extensions: [ext("demo", "tools", 1, "Demo")] }),
        "DELETE /api/extensions/demo": () => json(500, { error: { code: "internal", message: "boom" } }),
      },
      () => <ExtensionsPage />,
    );
    await view.shell.session.load();
    await view.shell.extensions.reload();
    fireEvent.click(view.getByRole("button", { name: "Remove" }));
    const dialog = await view.findByRole("dialog", { name: "Remove extension" });
    fireEvent.click(dialog.querySelector(".btn-error")!);
    await waitFor(() => expect(view.shell.toasts.items()[0]?.message).toBe("Could not remove Demo: boom"));
    expect(view.shell.extensions.list()).toHaveLength(1);
  });

  it("asks for admin mode first and does nothing when it is declined", async () => {
    const view = mount(
      {
        "GET /api/session": () => json(200, sessionInfo({ canAdmin: true })),
        "GET /api/extensions": () => json(200, { extensions: [ext("demo", "tools", 1, "Demo")] }),
      },
      () => <ExtensionsPage />,
    );
    await view.shell.session.load();
    await view.shell.extensions.reload();
    fireEvent.click(view.getByRole("button", { name: "Remove" }));
    await waitFor(() => expect(view.shell.session.adminRequested()).toBe(true));
    view.shell.session.answerAdminRequest(false);
    await settle();
    expect(view.queryByRole("dialog")).toBeNull();
    expect(view.fetch.calls.some((c) => c.method === "DELETE")).toBe(false);
  });

  it("shows a load error and retries", async () => {
    let failing = true;
    const view = mount(
      {
        "GET /api/extensions": () =>
          failing
            ? json(500, { error: { code: "internal", message: "boom" } })
            : json(200, { extensions: [ext("demo", "tools", 1, "Demo")] }),
      },
      () => <ExtensionsPage />,
    );
    await view.shell.extensions.reload();
    expect((await view.findByRole("alert")).textContent).toContain("boom");
    failing = false;
    fireEvent.click(view.getByRole("button", { name: "Retry" }));
    expect(await view.findByText("Demo")).toBeTruthy();
    expect(view.queryByRole("alert")).toBeNull();
  });
});

describe("toasts", () => {
  it("shows messages by kind and dismisses them", () => {
    const view = mount({}, () => <Toasts />);
    view.shell.toasts.success("Saved");
    view.shell.toasts.error("Broke");
    const [saved, broke] = view.getAllByRole("status");
    expect(saved?.textContent).toContain("Saved");
    expect(saved?.className).toContain("alert-success");
    expect(broke?.className).toContain("alert-error");

    fireEvent.click(saved!.querySelector("button")!);
    expect(view.getAllByRole("status")).toHaveLength(1);
    expect(view.queryByText("Saved")).toBeNull();
  });
});
