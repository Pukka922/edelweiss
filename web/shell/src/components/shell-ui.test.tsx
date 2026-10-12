import { sessionInfo } from "@edelweiss/sdk/testing";
import { cleanup, fireEvent, waitFor } from "@solidjs/testing-library";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { ExtensionsPage } from "../pages/Extensions";
import { type Handler, json } from "../test/fake-fetch";
import { ext, noUpdates } from "../test/fixtures";
import { mount, settle } from "../test/mount";
import { AdminDialog } from "./AdminDialog";
import { Layout } from "./Layout";
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

  // containers became virtualization in edelweiss 0.2.0: the section sits between storage and network
  it("sidebar shows the section virtualization as Containers & VMs, and in German as Container & VMs", async () => {
    const view = mount(
      {
        "GET /api/extensions": () =>
          json(200, {
            extensions: [
              ext("net", "network", 1, "Net"),
              ext("docker", "virtualization", 10, "Docker"),
              ext("disk", "storage", 1, "Disk"),
            ],
          }),
      },
      () => <Sidebar />,
    );
    await view.shell.extensions.reload();
    const headings = () => view.getAllByRole("heading").map((h) => h.textContent);
    expect(headings()).toEqual(["Storage", "Containers & VMs", "Network", "Settings"]);
    expect(view.getByRole("link", { name: "Docker" }).getAttribute("href")).toBe("/x/docker");
    view.shell.setLocale("de");
    expect(headings()).toEqual(["Speicher", "Container & VMs", "Netzwerk", "Einstellungen"]);
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
    fireEvent.click(view.getByRole("button", { name: "Remove Demo" }));
    const dialog = await view.findByRole("dialog", { name: "Remove extension" });
    fireEvent.click(dialog.querySelector(".btn-error")!);
    // the toast follows the list update by a few microtasks: wait for the last thing that happens
    await waitFor(() => expect(view.shell.toasts.items()[0]?.message).toBe("Demo was removed."));
    expect(view.shell.extensions.list()).toEqual([]);
  });
});

describe("layout", () => {
  // shell-main (style.css) is p-4 plus lg:p-6; p-4 on the element itself would be overridden by the p-4 of an
  // extension's style sheet, which comes later in the same cascade layer, and the page would keep 1 rem on desktop
  it("pads the page through shell-main, not through a padding utility an extension style sheet could override", async () => {
    const view = mount({ "GET /api/session": () => json(200, sessionInfo()) }, () => (
      <Layout>
        <p>content</p>
      </Layout>
    ));
    await view.shell.session.load();
    const main = view.getByRole("main");
    expect(main.textContent).toBe("content");
    expect(main.classList.contains("shell-main")).toBe(true);
    expect(main.classList.contains("p-4")).toBe(false);
    expect(main.classList.contains("lg:p-6")).toBe(false);
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

  it("puts language, theme and sign-out into the user's dropdown, the admin button stays outside", async () => {
    const view = mount(signedIn, () => <Navbar />);
    await view.shell.session.load();
    const menu = view.getByText("alice").closest("details");
    expect(menu).not.toBeNull();
    const within = (label: string) => menu?.contains(view.getByLabelText(label)) ?? false;
    expect(within("Language")).toBe(true);
    expect(within("Theme")).toBe(true);
    expect(menu?.contains(view.getByRole("button", { name: "Sign out" }))).toBe(true);
    expect(menu?.contains(view.getByRole("button", { name: /Admin mode/ }))).toBe(false);
  });

  it("puts the Host menu between the admin button and the user menu", async () => {
    const view = mount(signedIn, () => <Navbar />);
    await view.shell.session.load();
    const admin = view.getByRole("button", { name: /Admin mode/ });
    const host = view.getByText("Host").closest("details") as HTMLElement;
    const user = view.getByText("alice").closest("details") as HTMLElement;
    expect(host).not.toBeNull();
    expect(admin.compareDocumentPosition(host) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(host.compareDocumentPosition(user) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  // Language and theme sit in rows of the user's dropdown: the visible text names its select, so a click on the text
  // reaches it. The selects keep their aria-label for the login page, where they stand alone.
  it("associates the visible text of the language and theme rows with their selects", async () => {
    const view = mount(signedIn, () => <Navbar />);
    await view.shell.session.load();
    const ids = new Set<string>();
    for (const name of ["Language", "Theme"]) {
      const select = view.getByRole("combobox", { name });
      const row = view.getByText(name).closest("label");
      expect(row?.control).toBe(select);
      expect(row?.htmlFor).toBe(select.id);
      expect(select.getAttribute("aria-label")).toBe(name);
      ids.add(select.id);
    }
    expect(ids.size).toBe(2);
    expect(ids.has("")).toBe(false);
  });

  // The login page shows the selects without a label: no id there, only the aria-label.
  it("leaves the id off a select that is not given one", () => {
    const view = mount({}, () => (
      <>
        <LanguageSelect />
        <ThemeSelect />
      </>
    ));
    for (const name of ["Language", "Theme"]) {
      expect(view.getByRole("combobox", { name }).hasAttribute("id")).toBe(false);
    }
  });

  // The menus close themselves (pointer, focus, Escape: see dropdown.test.tsx) and never both stay open, but a
  // choice in the user's menu keeps it open: language and theme are often changed together.
  it("keeps one menu open at a time, and the user menu stays open when a setting is changed", async () => {
    const view = mount(signedIn, () => <Navbar />);
    await view.shell.session.load();
    const host = view.getByText("Host").closest("details") as HTMLDetailsElement;
    const user = view.getByText("alice").closest("details") as HTMLDetailsElement;

    fireEvent.pointerDown(view.getByText("alice"));
    fireEvent.click(view.getByText("alice"));
    expect(user.open).toBe(true);
    // What a click on a select amounts to for the document: the pointer goes down, the select takes the focus, the
    // value changes (a bare change event would not reach the handlers that close menus on a pointer or focus outside).
    const choose = (name: string, value: string) => {
      const select = view.getByRole("combobox", { name });
      fireEvent.pointerDown(select);
      fireEvent.focusIn(select);
      fireEvent.change(select, { target: { value } });
      expect(user.open).toBe(true);
    };
    choose("Theme", "dark");
    choose("Language", "de");
    expect(view.shell.theme()).toBe("dark");
    expect(view.shell.i18n.locale()).toBe("de");
    expect(user.open).toBe(true);

    fireEvent.pointerDown(view.getByText("Host"));
    fireEvent.click(view.getByText("Host"));
    expect(host.open).toBe(true);
    expect(user.open).toBe(false);

    fireEvent.keyDown(document, { key: "Escape" });
    expect(host.open).toBe(false);
  });

  // Like the host menu's, the user's summary shows the icon and, from sm up, the name (always for screen readers).
  it("shows the user menu's icon to the eye only, and its name to assistive technology and from sm up", async () => {
    const view = mount(signedIn, () => <Navbar />);
    await view.shell.session.load();
    const name = view.getByText("alice");
    const icon = name.closest("summary")?.querySelector("svg");
    expect(icon?.closest("[aria-hidden]")?.getAttribute("aria-hidden")).toBe("true");
    // shell-label (style.css) is sr-only plus sm:not-sr-only; the classes are not on the element itself, where the
    // sr-only of an extension's style sheet would override the variant
    expect(name.classList.contains("shell-label")).toBe(true);
    expect(name.classList.contains("sr-only")).toBe(false);
    expect(name.classList.contains("sm:not-sr-only")).toBe(false);
  });

  // The selects sit next to their text in the dropdown's rows and must keep the width their text and chevron need;
  // on a phone the crowded navbar row makes the admin toggle give up its label (not for screen readers).
  it("keeps the selects at their content width and the admin label readable for assistive technology", async () => {
    const view = mount(signedIn, () => <Navbar />);
    await view.shell.session.load();
    for (const name of ["Language", "Theme"]) {
      expect(view.getByRole("combobox", { name }).classList.contains("shrink-0")).toBe(true);
    }
    const label = view.getByRole("button", { name: "Admin mode" }).querySelector("span")!;
    expect(label.classList.contains("shell-label")).toBe(true);
    expect(label.classList.contains("sr-only")).toBe(false);
    expect(label.classList.contains("sm:not-sr-only")).toBe(false);
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
    expect((view.getByRole("button", { name: "Remove Live" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("offers removal only to users who are in or may enter admin mode", async () => {
    const routes = {
      "GET /api/extensions": () => json(200, { extensions: [ext("demo", "tools", 1, "Demo")] }),
    };
    const removeButton = (view: ReturnType<typeof mount>) =>
      view.getByRole("button", { name: "Remove Demo" }) as HTMLButtonElement;

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
    fireEvent.click(view.getByRole("button", { name: "Remove Demo" }));
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
    fireEvent.click(view.getByRole("button", { name: "Remove Demo" }));
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
    fireEvent.click(view.getByRole("button", { name: "Remove Demo" }));
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
