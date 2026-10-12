import { sessionInfo } from "@edelweiss/sdk/testing";
import { cleanup, fireEvent, screen, waitFor, within } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import { json } from "../test/fake-fetch";
import { mount } from "../test/mount";
import { AdminDialog } from "./AdminDialog";
import { HostMenu } from "./HostMenu";

/** Where HostMenu goes once the restarted host answers again (the real one navigates the browser). */
const toLogin = vi.fn();

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  toLogin.mockClear();
});

// The dialog and the page over everything are rendered into the body (a Portal), outside of the container the view's
// queries search: the tests look through screen.
const pi = { hostname: "pi" };
const answers = (info: Parameters<typeof sessionInfo>[0]) => ({
  "GET /api/session": () => json(200, sessionInfo({ ...pi, ...info })),
});
/** A user in admin mode, and the host accepting a restart. */
const inAdminMode = {
  ...answers({ canAdmin: true, admin: true }),
  "POST /api/system/reboot": () => json(202, { action: "reboot" }),
};

/** The shell's own fetch is the fake one of mount(); the host's health is read with the global fetch. */
function stubHealth(impl: (init?: RequestInit) => Promise<Response>) {
  const health = vi.fn((_url: string, init?: RequestInit) => impl(init));
  vi.stubGlobal("fetch", health);
  return health;
}

const healthy = async () => new Response(JSON.stringify({ status: "ok" }), { status: 200 });

const summary = () => screen.getByText("Host").closest("summary") as HTMLElement;

/** Chooses Restart in the menu and confirms in the dialog. */
async function restart() {
  fireEvent.click(screen.getByRole("button", { name: "Restart" }));
  const dialog = await screen.findByRole("dialog", { name: "Restart host" });
  fireEvent.click(within(dialog).getByRole("button", { name: "Restart" }));
}

/** Chooses Restart in the menu; the dialog is open afterwards. */
async function askToRestart() {
  fireEvent.click(screen.getByRole("button", { name: "Restart" }));
  return screen.findByRole("dialog", { name: "Restart host" });
}

const wait = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

describe("HostMenu", () => {
  it("shows the entries greyed out to those who may not use admin mode", async () => {
    const view = mount(answers({ canAdmin: false }), () => <HostMenu />);
    await view.shell.session.load();
    const restart = screen.getByRole("button", { name: /Restart/ }) as HTMLButtonElement;
    const shutDown = screen.getByRole("button", { name: /Shut down/ }) as HTMLButtonElement;
    expect(restart.disabled).toBe(true);
    expect(shutDown.disabled).toBe(true);
    expect(screen.getAllByText("Only for administrators")).toHaveLength(2);
    fireEvent.click(restart);
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  // The menu item is a grid with one column for each child: label and hint in separate columns squeezed "Shut down"
  // onto two lines (and in German pushed the hint out of the menu). One cell holds both, the hint below the label.
  it("keeps the label and the hint of an entry in one cell, the label on one line", async () => {
    const view = mount(answers({ canAdmin: false }), () => <HostMenu />);
    await view.shell.session.load();
    const shutDown = screen.getByRole("button", { name: /Shut down/ });
    const label = within(shutDown).getByText("Shut down");
    const cell = label.parentElement as HTMLElement;
    expect(cell.parentElement).toBe(shutDown);
    expect(cell.classList.contains("flex-col")).toBe(true);
    expect(label.classList.contains("whitespace-nowrap")).toBe(true);
    expect(within(cell).getByText("Only for administrators")).toBeTruthy();
  });

  // shell-label (style.css) is sr-only plus sm:not-sr-only; the classes are not on the element itself, where the
  // sr-only of an extension's style sheet would override the variant
  it("shows the summary's icon to the eye only, and its label to assistive technology and from sm up", async () => {
    const view = mount(answers({ canAdmin: true }), () => <HostMenu />);
    await view.shell.session.load();
    const label = screen.getByText("Host");
    const icon = label.closest("summary")?.querySelector("svg");
    expect(icon?.closest("[aria-hidden]")?.getAttribute("aria-hidden")).toBe("true");
    expect(label.classList.contains("shell-label")).toBe(true);
    expect(label.classList.contains("sr-only")).toBe(false);
    expect(label.classList.contains("sm:not-sr-only")).toBe(false);
  });

  it("asks for admin mode first, then confirms with the host name, then restarts and waits for the host", async () => {
    const view = mount(
      {
        ...answers({ canAdmin: true }),
        "POST /api/session/admin": () => json(200, sessionInfo({ ...pi, admin: true })),
        "POST /api/system/reboot": () => json(202, { action: "reboot" }),
      },
      () => (
        <>
          <HostMenu pollMs={10} onBack={toLogin} />
          <AdminDialog />
        </>
      ),
    );
    // down for the first answer, then up again
    let asked = 0;
    const health = stubHealth(async () => {
      if (++asked === 1) throw new TypeError("down");
      return healthy();
    });
    await view.shell.session.load();

    fireEvent.click(screen.getByRole("button", { name: "Restart" }));
    const admin = await screen.findByRole("dialog", { name: "Administrative access" });
    expect(view.fetch.calls.some((c) => c.url === "/api/system/reboot")).toBe(false);
    fireEvent.input(screen.getByLabelText("Password"), { target: { value: "pw" } });
    fireEvent.submit(admin.querySelector("form")!);

    const dialog = await screen.findByRole("dialog", { name: "Restart host" });
    expect(dialog.textContent).toContain("Restart the host pi?");
    expect(view.fetch.calls.some((c) => c.url === "/api/system/reboot")).toBe(false);
    fireEvent.click(within(dialog).getByRole("button", { name: "Restart" }));

    expect(await screen.findByText("The host pi is restarting …")).toBeTruthy();
    expect(screen.queryByRole("dialog")).toBeNull();
    await waitFor(() => expect(toLogin).toHaveBeenCalledTimes(1));
    expect(health).toHaveBeenCalledTimes(2);
    // every check has a timeout of its own, so that a request that hangs cannot stall the loop
    expect(health.mock.calls[0]).toEqual([
      "/api/health",
      { cache: "no-store", signal: expect.any(AbortSignal) },
    ]);
    const post = view.fetch.calls.find((c) => c.url === "/api/system/reboot");
    expect(post?.method).toBe("POST");
  });

  it("shows that the host was shut down, without waiting for it", async () => {
    const health = stubHealth(healthy);
    const view = mount(
      {
        ...answers({ canAdmin: true, admin: true }),
        "POST /api/system/poweroff": () => json(202, { action: "poweroff" }),
      },
      () => <HostMenu pollMs={10} onBack={toLogin} />,
    );
    await view.shell.session.load();
    fireEvent.click(screen.getByRole("button", { name: /Shut down/ }));
    const dialog = await screen.findByRole("dialog", { name: "Shut down host" });
    expect(dialog.textContent).toContain("Shut down the host pi?");
    fireEvent.click(within(dialog).getByRole("button", { name: "Shut down" }));

    expect(await screen.findByText("The host pi was shut down.")).toBeTruthy();
    await wait(50); // several poll intervals
    expect(health).not.toHaveBeenCalled();
    expect(toLogin).not.toHaveBeenCalled();
  });

  it("closes its menu before the confirmation opens", async () => {
    const view = mount(inAdminMode, () => <HostMenu />);
    await view.shell.session.load();
    const menu = screen.getByText("Host").closest("details") as HTMLDetailsElement;
    menu.open = true;

    await askToRestart();
    expect(menu.open).toBe(false);
  });

  it("does nothing when the confirmation is cancelled", async () => {
    const view = mount(inAdminMode, () => <HostMenu />);
    await view.shell.session.load();
    const dialog = await askToRestart();
    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));

    expect(screen.queryByRole("dialog")).toBeNull();
    expect(screen.queryByRole("status")).toBeNull();
    expect(view.fetch.calls.some((c) => c.url === "/api/system/reboot")).toBe(false);
  });

  // The entries hide the button that had the focus and the dialogs do not give it back: without this the focus would
  // fall to the page, and a keyboard user starts again at its top.
  describe("gives the focus back to the menu", () => {
    const ways: [string, () => void][] = [
      ["Cancel", () => fireEvent.click(screen.getByRole("button", { name: "Cancel" }))],
      ["Escape in the confirmation", () => fireEvent.keyDown(document, { key: "Escape" })],
    ];
    it.each(ways)("when the confirmation is left with %s", async (_name, leave) => {
      const view = mount(inAdminMode, () => <HostMenu />);
      await view.shell.session.load();
      await askToRestart();
      expect(document.activeElement).not.toBe(summary()); // the dialog has it (on Cancel)

      leave();
      expect(screen.queryByRole("dialog")).toBeNull();
      expect(document.activeElement).toBe(summary());
    });

    it("when admin mode is declined", async () => {
      const view = mount(answers({ canAdmin: true }), () => (
        <>
          <HostMenu />
          <AdminDialog />
        </>
      ));
      await view.shell.session.load();
      fireEvent.click(screen.getByRole("button", { name: "Restart" }));
      await screen.findByRole("dialog", { name: "Administrative access" });

      fireEvent.keyDown(document, { key: "Escape" });
      await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
      expect(document.activeElement).toBe(summary());
    });

    it("when the request fails", async () => {
      const view = mount(
        {
          ...answers({ canAdmin: true, admin: true }),
          "POST /api/system/reboot": () =>
            json(503, { error: { code: "power_unavailable", message: "not available" } }),
        },
        () => <HostMenu />,
      );
      await view.shell.session.load();
      await restart();

      await waitFor(() => expect(view.shell.toasts.items()).toHaveLength(1));
      expect(document.activeElement).toBe(summary());
    });
  });

  it("reports a failure and shows no page over the shell", async () => {
    const view = mount(
      {
        ...answers({ canAdmin: true, admin: true }),
        "POST /api/system/reboot": () =>
          json(503, { error: { code: "power_unavailable", message: "not available" } }),
      },
      () => <HostMenu />,
    );
    await view.shell.session.load();
    await restart();

    await waitFor(() =>
      expect(view.shell.toasts.items()[0]?.message).toBe(
        "The host could not be restarted or shut down: not available",
      ),
    );
    expect(screen.queryByRole("status")).toBeNull();
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(view.container.hasAttribute("inert")).toBe(false);
  });

  // The request takes a moment: the dialog stays, its buttons are disabled and a second click sends nothing.
  it("shows that the request is on its way and sends it once", async () => {
    let answer = (_response: Response) => {};
    const view = mount(
      {
        ...answers({ canAdmin: true, admin: true }),
        "POST /api/system/poweroff": () =>
          new Promise<Response>((resolve) => {
            answer = resolve;
          }),
      },
      () => <HostMenu />,
    );
    await view.shell.session.load();
    fireEvent.click(screen.getByRole("button", { name: /Shut down/ }));
    const dialog = await screen.findByRole("dialog", { name: "Shut down host" });
    const confirm = within(dialog).getByRole("button", { name: "Shut down" }) as HTMLButtonElement;
    const cancel = within(dialog).getByRole("button", { name: "Cancel" }) as HTMLButtonElement;

    fireEvent.click(confirm);
    fireEvent.click(confirm);
    expect(confirm.disabled && cancel.disabled).toBe(true);
    expect(confirm.getAttribute("aria-busy")).toBe("true");
    fireEvent.click(cancel);
    fireEvent.keyDown(document, { key: "Escape" });
    // the other entry, reached behind the dialog (with the keyboard), must not swap the question
    fireEvent.click(within(view.container).getByRole("button", { name: "Restart" }));
    expect(screen.getByRole("dialog", { name: "Shut down host" })).toBeTruthy();
    expect(screen.queryByRole("dialog", { name: "Restart host" })).toBeNull();
    expect(view.fetch.calls.filter((c) => c.url === "/api/system/poweroff")).toHaveLength(1);

    answer(json(202, { action: "poweroff" }));
    expect(await screen.findByText("The host pi was shut down.")).toBeTruthy();
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(view.fetch.calls.filter((c) => c.url === "/api/system/poweroff")).toHaveLength(1);
  });

  it("does not start waiting when the menu is gone before the answer arrives", async () => {
    let answer = (_response: Response) => {};
    const health = stubHealth(healthy);
    const view = mount(
      {
        ...answers({ canAdmin: true, admin: true }),
        "POST /api/system/reboot": () =>
          new Promise<Response>((resolve) => {
            answer = resolve;
          }),
      },
      () => <HostMenu pollMs={10} onBack={toLogin} />,
    );
    await view.shell.session.load();
    await restart();
    await waitFor(() => expect(view.fetch.calls.some((c) => c.url === "/api/system/reboot")).toBe(true));

    view.unmount();
    answer(json(202, { action: "reboot" }));
    await wait(50); // several poll intervals
    expect(health).not.toHaveBeenCalled();
    expect(toLogin).not.toHaveBeenCalled();
  });

  // The page over everything must not leave the app reachable: with the keyboard one could tab to the menu behind it
  // and send a second action.
  it("makes the app behind the page inert while it is shown, and not the page itself", async () => {
    stubHealth(healthy);
    const view = mount(
      {
        ...answers({ canAdmin: true, admin: true }),
        "POST /api/system/poweroff": () => json(202, { action: "poweroff" }),
      },
      () => <HostMenu />,
    );
    await view.shell.session.load();
    expect(view.container.hasAttribute("inert")).toBe(false);
    fireEvent.click(screen.getByRole("button", { name: /Shut down/ }));
    const dialog = await screen.findByRole("dialog", { name: "Shut down host" });
    expect(view.container.hasAttribute("inert")).toBe(false); // the dialog is a dialog, not the page
    fireEvent.click(within(dialog).getByRole("button", { name: "Shut down" }));

    const page = await screen.findByRole("status");
    expect(view.container.hasAttribute("inert")).toBe(true);
    expect(page.closest("[inert]")).toBeNull();

    view.unmount();
    expect(view.container.hasAttribute("inert")).toBe(false);
  });

  it("does not wait for a check that hangs: its timeout ends it and the loop goes on", async () => {
    // the first check never answers by itself; it ends with the abort of its timeout. The second one is answered.
    let asked = 0;
    const health = stubHealth((init) =>
      ++asked > 1
        ? healthy()
        : new Promise<Response>((_resolve, reject) => {
            init?.signal?.addEventListener("abort", () => reject(new TypeError("aborted")));
          }),
    );
    const view = mount(inAdminMode, () => <HostMenu pollMs={10} probeMs={20} onBack={toLogin} />);
    await view.shell.session.load();
    await restart();

    await waitFor(() => expect(toLogin).toHaveBeenCalledTimes(1)); // down (the timeout), then up
    expect(health).toHaveBeenCalledTimes(2);
  });

  describe("when the host keeps answering after a restart", () => {
    const stillUpMessage = "The host still answers; the restart may have failed.";

    /**
     * A confirmed restart. Every answer of the health check comes `step` ms after the previous one by the clock the
     * component reads (default: two minutes and more); `second` decides when the second one is given.
     */
    async function restarted({ step = 130_000, second = Promise.resolve() as Promise<unknown> } = {}) {
      const clock = { now: 1_000 };
      let asked = 0;
      vi.spyOn(Date, "now").mockImplementation(() => clock.now);
      const health = stubHealth(async () => {
        clock.now += step;
        if (++asked === 2) await second;
        return healthy();
      });
      const view = mount(inAdminMode, () => <HostMenu pollMs={10} onBack={toLogin} />);
      await view.shell.session.load();
      await restart();
      return { view, health, clock };
    }

    it("says that the restart may have failed and does not leave for the login page", async () => {
      await restarted();
      expect(await screen.findByText(stillUpMessage)).toBeTruthy();
      expect(toLogin).not.toHaveBeenCalled();
    });

    it("says so only after two minutes", async () => {
      const { health, clock } = await restarted({ step: 1_000 });
      await screen.findByText("The host pi is restarting …");
      await waitFor(() => expect(health.mock.calls.length).toBeGreaterThanOrEqual(4));
      expect(clock.now).toBeLessThan(1_000 + 120_000);
      expect(screen.queryByText(stillUpMessage)).toBeNull();
      expect(screen.queryByRole("button", { name: "Close" })).toBeNull();

      clock.now += 120_000; // the wait has taken more than two minutes now
      expect(await screen.findByText(stillUpMessage)).toBeTruthy();
      expect(toLogin).not.toHaveBeenCalled();
    });

    it("lets the app work again when the page is closed", async () => {
      const { view } = await restarted();
      await screen.findByText(stillUpMessage);
      expect(view.container.hasAttribute("inert")).toBe(true);

      fireEvent.click(screen.getByRole("button", { name: "Close" }));
      expect(screen.queryByRole("status")).toBeNull();
      expect(view.container.hasAttribute("inert")).toBe(false);
    });

    // The second answer is on its way when the page is closed or the menu goes: it must not start a third check.
    const ways: [string, (view: { unmount: () => void }) => void][] = [
      ["the page is closed", () => fireEvent.click(screen.getByRole("button", { name: "Close" }))],
      ["the menu is gone", (view) => view.unmount()],
    ];
    it.each(ways)("stops waiting when %s", async (_name, leave) => {
      let answer = () => {};
      const { view, health } = await restarted({
        second: new Promise<void>((resolve) => {
          answer = resolve;
        }),
      });
      await screen.findByText(stillUpMessage);
      await waitFor(() => expect(health).toHaveBeenCalledTimes(2));

      leave(view);
      answer();
      await wait(50); // several poll intervals
      expect(health).toHaveBeenCalledTimes(2);
      expect(screen.queryByRole("status")).toBeNull();
    });
  });
});
