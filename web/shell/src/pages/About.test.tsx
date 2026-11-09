import { sessionInfo } from "@edelweiss/sdk/testing";
import { cleanup, fireEvent, screen, waitFor, within } from "@solidjs/testing-library";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AboutInfo, CheckView } from "../about";
import { json } from "../test/fake-fetch";
import { mount, settle } from "../test/mount";
import { AboutPage } from "./About";

beforeEach(() => {
  vi.spyOn(window, "scrollTo").mockImplementation(() => {});
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

const SUM = "ab".repeat(32);
const admin = () => json(200, sessionInfo({ admin: true }));
const user = () => json(200, sessionInfo({ admin: false, canAdmin: true }));

const info = (over: Partial<AboutInfo> = {}): AboutInfo => ({
  version: "0.3.1",
  arch: "amd64",
  mode: "sys",
  possible: true,
  repo: "Pukka922/edelweiss",
  state: { running: false },
  ...over,
});
const view = (over: Partial<CheckView> = {}): CheckView => ({
  offer: { current: "0.3.1", available: false },
  checkedAt: new Date().toISOString(),
  ageSeconds: 0,
  ...over,
});
const offer = (): CheckView =>
  view({
    offer: {
      current: "0.3.1",
      latest: "0.4.0",
      available: true,
      release: "v0.4.0",
      published: "2026-11-01T12:00:00Z",
      notesUrl: "https://github.com/Pukka922/edelweiss/releases/tag/v0.4.0",
      asset: "edelweissd_0.4.0_linux_amd64",
      size: 12_345_678,
      sha256: SUM,
    },
  });

async function open(
  routes: Record<string, () => Response | Promise<Response>>,
  props: Parameters<typeof AboutPage>[0] = {},
) {
  const v = mount(routes, () => <AboutPage {...props} />);
  await v.shell.session.load(); // the shell does this before it shows any page
  return v;
}
const inAdmin = (about: AboutInfo, check: CheckView | (() => Response) = view()) => ({
  "GET /api/session": admin,
  "GET /api/about": () => json(200, about),
  "GET /api/about/update": typeof check === "function" ? check : () => json(200, check),
  "GET /api/about/update?refresh=1": typeof check === "function" ? check : () => json(200, check),
});
const asked = (v: { fetch: { calls: { method: string; url: string }[] } }, path: string, method = "GET") =>
  v.fetch.calls.filter((c) => c.method === method && c.url === path).length;

describe("about page", () => {
  it("shows the version, the architecture and the mode, and links to the repository and the license", async () => {
    const v = await open({
      "GET /api/session": user,
      "GET /api/about": () => json(200, info({ mode: "diskless" })),
    });
    expect(await v.findByText("0.3.1")).toBeTruthy();
    expect(v.getByRole("heading", { name: "edelweiss" })).toBeTruthy(); // the product, above the version
    expect(v.getByText("amd64")).toBeTruthy();
    expect(v.getByText("diskless")).toBeTruthy();
    expect((v.getByRole("link", { name: "Repository on GitHub" }) as HTMLAnchorElement).href).toBe(
      "https://github.com/Pukka922/edelweiss",
    );
    const license = v.getByRole("link", { name: "License (MIT)" }) as HTMLAnchorElement;
    expect(license.href).toBe("https://github.com/Pukka922/edelweiss/blob/main/LICENSE");
    expect(license.rel).toContain("noopener");
  });

  it("calls a development build a development version and does not look for updates", async () => {
    const v = await open(inAdmin(info({ version: "0.0.0-dev", possible: false, reason: "dev_build" })));
    expect(await v.findByText("Development version")).toBeTruthy();
    await settle();
    expect(asked(v, "/api/about/update")).toBe(0);
  });

  it.each([
    ["dev_mode", /development mode/],
    ["not_service", /not run as an OpenRC service/],
    ["no_openrc", /OpenRC is not available/],
    ["not_writable", /not writable/],
    ["unsupported_arch", /no release for this architecture/],
    ["unknown_mode", /installation mode/],
    ["no_lbu", /lbu is not available/],
    ["listen_address", /listen address/],
  ])("tells why it cannot update here: %s", async (reason, text) => {
    const v = await open({
      "GET /api/session": user,
      "GET /api/about": () => json(200, info({ possible: false, reason })),
    });
    expect(await v.findByText(text)).toBeTruthy();
    expect(v.getByText(/cannot update itself here/)).toBeTruthy();
    expect((v.getByRole("link", { name: "README" }) as HTMLAnchorElement).href).toContain("#readme");
    expect(v.queryByRole("button", { name: "Update" })).toBeNull();
    expect(v.queryByRole("button", { name: "Check for updates" })).toBeNull();
  });

  it("shows the code for a reason it does not know", async () => {
    const v = await open({
      "GET /api/session": user,
      "GET /api/about": () => json(200, info({ possible: false, reason: "brand_new" })),
    });
    expect(await v.findByText(/brand_new/)).toBeTruthy();
  });

  it("asks for admin mode before checking and does nothing when it is declined", async () => {
    const v = await open({ "GET /api/session": user, "GET /api/about": () => json(200, info()) });
    fireEvent.click(await v.findByRole("button", { name: "Check for updates" }));
    await waitFor(() => expect(v.shell.session.adminRequested()).toBe(true));
    v.shell.session.answerAdminRequest(false);
    await settle();
    expect(asked(v, "/api/about/update")).toBe(0);
    expect(asked(v, "/api/about/update?refresh=1")).toBe(0);
  });

  it("checks when it is opened in admin mode and says it is up to date", async () => {
    const v = await open(inAdmin(info()));
    expect(await v.findByText("Up to date.")).toBeTruthy();
    expect(asked(v, "/api/about/update")).toBe(1);
    expect(v.getByText("Checked just now")).toBeTruthy();
  });

  it("checks again on demand and asks GitHub", async () => {
    const v = await open(inAdmin(info()));
    await v.findByText("Up to date.");
    fireEvent.click(v.getByRole("button", { name: "Check again" }));
    await waitFor(() => expect(asked(v, "/api/about/update?refresh=1")).toBe(1));
  });

  it("offers the update with the release notes in a new tab", async () => {
    const v = await open(inAdmin(info(), offer()));
    expect(await v.findByText(/Version 0\.4\.0 is available \(published/)).toBeTruthy();
    const notes = v.getByRole("link", { name: "Release notes" }) as HTMLAnchorElement;
    expect(notes.href).toBe("https://github.com/Pukka922/edelweiss/releases/tag/v0.4.0");
    expect(notes.target).toBe("_blank");
    expect(notes.rel).toContain("noopener");
    expect(v.getByRole("button", { name: "Update" })).toBeTruthy();
  });

  it("says that GitHub does not answer and which check it shows", async () => {
    const v = await open(
      inAdmin(
        info(),
        view({ stale: true, staleCode: "github_rate_limited", staleReason: "limit", ageSeconds: 600 }),
      ),
    );
    const note = await v.findByRole("status");
    expect(note.textContent).toContain("GitHub does not answer right now.");
    expect(note.textContent).toContain("GitHub's request limit is reached");
    expect(note.textContent).not.toMatch(/[()]/); // the reason is a short phrase, not a sentence in parentheses
    expect(v.getByText("Checked 10 minutes ago")).toBeTruthy();
  });

  describe("result of the last update", () => {
    const result = (over: object) => ({
      from: "0.3.1",
      to: "0.4.0",
      outcome: "rolled_back",
      at: "2026-11-05T10:00:00Z",
      ...over,
    });
    const withResult = (r: object, extra: Partial<AboutInfo> = {}) =>
      inAdmin(info({ state: { running: false, result: r as never }, ...extra }));

    it("says what was updated", async () => {
      const v = await open(withResult(result({ outcome: "updated" })));
      expect(await v.findByText(/was updated from 0\.3\.1 to 0\.4\.0/)).toBeTruthy();
    });

    it("says that it was rolled back, and why", async () => {
      const v = await open(withResult(result({ reason: "not_healthy" })));
      const note = (await v.findByText(/was rolled back/)).closest("[role=alert]") as HTMLElement;
      expect(note.textContent).toMatch(/did not report healthy/);
      expect(note.textContent).toContain("0.4.0");
    });

    it("points to the syslog when the rollback failed too", async () => {
      const v = await open(withResult(result({ outcome: "rollback_failed", reason: "restart_failed" })));
      expect(await v.findByText(/rollback failed too/)).toBeTruthy();
      expect(v.getByText(/logread/)).toBeTruthy();
      expect(v.getByText(/service could not be restarted/)).toBeTruthy();
    });

    it("says that it failed, and names a reason it does not know by its code", async () => {
      const v = await open(withResult(result({ outcome: "failed", reason: "strange_thing" })));
      expect(await v.findByText(/update from 0\.3\.1 to 0\.4\.0 failed/)).toBeTruthy();
      expect(v.getByText(/strange_thing/)).toBeTruthy();
    });

    it("names an outcome it does not know by its code", async () => {
      const v = await open(withResult(result({ outcome: "odd" })));
      expect(await v.findByText(/odd/)).toBeTruthy();
    });

    it("tells that lbu did not save the update, and what lbu said", async () => {
      const v = await open(
        withResult(result({ outcome: "updated", lbuFailed: true, lbuError: "lbu: no space" })),
      );
      expect(await v.findByText(/does not survive a restart of the host/)).toBeTruthy();
      expect(v.getByText(/lbu: no space/)).toBeTruthy();
    });

    // after the update everybody is signed out and signs in again without admin mode: the core tells that lbu failed
    // (lbuFailed), not what it said, and the page loads the answer again when admin mode is on
    it("tells outside admin mode that lbu failed and shows its message once admin mode is on", async () => {
      let admin = false;
      const v = await open({
        "GET /api/session": user,
        "POST /api/session/admin": () => {
          admin = true;
          return json(200, sessionInfo({ admin: true }));
        },
        "GET /api/about/update": () => json(200, view()),
        "GET /api/about": () =>
          json(
            200,
            info({
              state: {
                running: false,
                result: result({
                  outcome: "updated",
                  lbuFailed: true,
                  ...(admin ? { lbuError: "lbu: no space" } : {}),
                }) as never,
              },
            }),
          ),
      });
      expect(await v.findByText(/does not survive a restart of the host/)).toBeTruthy();
      expect(v.queryByText(/lbu: no space/)).toBeNull();
      await v.shell.session.enableAdmin("secret");
      expect(await v.findByText(/lbu: no space/)).toBeTruthy();
      expect(v.getByText(/does not survive a restart of the host/)).toBeTruthy();
    });

    it("leaves the note to those who can change things", async () => {
      const v = await open({
        "GET /api/session": () => json(200, sessionInfo({ admin: false, canAdmin: false })),
        "GET /api/about": () =>
          json(200, info({ state: { running: false, result: result({ outcome: "updated" }) as never } })),
      });
      expect(await v.findByText(/was updated from/)).toBeTruthy();
      expect(v.queryByRole("button", { name: "Close" })).toBeNull();
    });

    it("closes the note", async () => {
      let state: AboutInfo["state"] = { running: false, result: result({ outcome: "updated" }) as never };
      const v = await open({
        ...inAdmin(info()),
        "GET /api/about": () => json(200, info({ state })),
        "DELETE /api/about/update/result": () => {
          state = { running: false };
          return json(204);
        },
      });
      fireEvent.click(await v.findByRole("button", { name: "Close" }));
      await waitFor(() => expect(v.queryByText(/was updated from/)).toBeNull());
      expect(asked(v, "/api/about/update/result", "DELETE")).toBe(1);
    });
  });

  it("shows that an update is running and asks again until it ends", async () => {
    let running = true;
    const v = await open(
      {
        ...inAdmin(info()),
        "GET /api/about": () =>
          json(
            200,
            info({
              state: running
                ? { running: true }
                : { running: false, result: { from: "0.3.1", to: "0.4.0", outcome: "updated", at: "x" } },
            }),
          ),
      },
      { pollMs: 10 },
    );
    expect(await v.findByText("Checking the update …")).toBeTruthy();
    expect(v.queryByRole("button", { name: "Update" })).toBeNull();
    await waitFor(() => expect(asked(v, "/api/about")).toBeGreaterThanOrEqual(3));
    running = false;
    expect(await v.findByText(/was updated from/)).toBeTruthy();
    expect(v.queryByText("Checking the update …")).toBeNull();
  });

  describe("the update", () => {
    const healthAnswers = (...started: (string | Error)[]) => {
      const health = vi.fn(async () => {
        const next = started.length > 1 ? started.shift() : started[0];
        if (next instanceof Error) throw next;
        return new Response(JSON.stringify({ status: "ok", version: "x", started: next }), { status: 200 });
      });
      vi.stubGlobal("fetch", health);
      return health;
    };
    const down = () => new TypeError("down");

    // an update that was started runs (state.running) until the old edelweiss is gone
    let posted = false;
    beforeEach(() => {
      posted = false;
    });

    async function confirmed(extra: Record<string, () => Response | Promise<Response>> = {}, props = {}) {
      const v = await open(
        {
          ...inAdmin(info(), offer()),
          "GET /api/about": () => json(200, info({ state: { running: posted } })),
          "POST /api/about/update": () => {
            posted = true;
            return json(202, { to: "0.4.0" });
          },
          ...extra,
        },
        { pollMs: 10, probeMs: 50, ...props },
      );
      fireEvent.click(await v.findByRole("button", { name: "Update" }));
      const dialog = await screen.findByRole("dialog", { name: "Update edelweiss" });
      fireEvent.click(within(dialog).getByRole("button", { name: "Update" }));
      return v;
    }

    it("starts with the confirmed checksum, waits for the new start and goes to the login page", async () => {
      const health = healthAnswers("t1", down(), "t2");
      const onBack = vi.fn();
      const v = await confirmed({}, { onBack });
      await waitFor(() => expect(onBack).toHaveBeenCalledTimes(1));
      const post = v.fetch.calls.find((c) => c.method === "POST" && c.url === "/api/about/update");
      expect(post?.body).toEqual({ sha256: SUM });
      expect(health.mock.calls.length).toBeGreaterThanOrEqual(3); // the one before, then the waiting
      expect(await screen.findByText("edelweiss is restarting …")).toBeTruthy();
    });

    it("says that it downloads and checks until the core has accepted it", async () => {
      healthAnswers("t1");
      let accept = () => {};
      let posted = false;
      await confirmed({
        "POST /api/about/update": () =>
          new Promise<Response>((resolve) => {
            posted = true;
            accept = () => resolve(json(202, { to: "0.4.0" }));
          }),
      });
      expect(await screen.findByText("Downloading and checking …")).toBeTruthy();
      await waitFor(() => expect(posted).toBe(true));
      accept();
      expect(await screen.findByText("edelweiss is restarting …")).toBeTruthy();
    });

    it("keeps the dialog and does not ask for the info again when the session is refreshed while admin mode stays on", async () => {
      healthAnswers("t1");
      let accept = () => {};
      const v = await confirmed({
        "POST /api/about/update": () =>
          new Promise<Response>((resolve) => {
            accept = () => resolve(json(202, { to: "0.4.0" }));
          }),
      });
      expect(await screen.findByText("Downloading and checking …")).toBeTruthy();
      const before = asked(v, "/api/about");
      await v.shell.session.load(); // a refresh on window focus: the session info is replaced, admin stays on
      await settle();
      expect(asked(v, "/api/about")).toBe(before);
      expect(screen.getByText("Downloading and checking …")).toBeTruthy();
      expect(screen.getByRole("dialog", { name: "Update edelweiss" })).toBeTruthy();
      accept();
      expect(await screen.findByText("edelweiss is restarting …")).toBeTruthy();
    });

    it("words a refusal per code in the dialog and keeps it open", async () => {
      healthAnswers("t1");
      await confirmed({
        "POST /api/about/update": () => json(409, { error: { code: "release_changed", message: "changed" } }),
      });
      const dialog = await screen.findByRole("dialog", { name: "Update edelweiss" });
      expect(await within(dialog).findByRole("alert")).toBeTruthy();
      expect(within(dialog).getByRole("alert").textContent).toMatch(/release changed after you reviewed it/);
      expect(screen.queryByText("edelweiss is restarting …")).toBeNull();
    });

    it("words a not_possible refusal with its reason", async () => {
      healthAnswers("t1");
      await confirmed({
        "POST /api/about/update": () =>
          json(409, { error: { code: "not_possible", message: "no", reason: "no_openrc" } }),
      });
      const dialog = await screen.findByRole("dialog", { name: "Update edelweiss" });
      expect((await within(dialog).findByRole("alert")).textContent).toMatch(/OpenRC is not available/);
    });

    it("falls back to the generic text for a code it does not know", async () => {
      healthAnswers("t1");
      await confirmed({
        "POST /api/about/update": () =>
          json(500, { error: { code: "new_code", message: "the core says so" } }),
      });
      const dialog = await screen.findByRole("dialog", { name: "Update edelweiss" });
      expect((await within(dialog).findByRole("alert")).textContent).toBe("the core says so");
    });

    it("points to the syslog and the helper log when edelweiss does not come back", async () => {
      healthAnswers("t1");
      const v = await confirmed({}, { timeoutMs: 150 });
      const note = await screen.findByText(/edelweiss does not answer/);
      expect(note.textContent).toMatch(/logread/);
      expect(note.textContent).toContain("/var/lib/edelweiss/self-update/helper.log");
      expect(v.container.hasAttribute("inert")).toBe(true);
      fireEvent.click(screen.getByRole("button", { name: "Close" }));
      expect(v.container.hasAttribute("inert")).toBe(false);
    });

    it("treats a lost answer to the start as unknown: it waits for the restart and shows no browser error", async () => {
      healthAnswers("t1", down(), "t2");
      const onBack = vi.fn();
      await confirmed(
        {
          "POST /api/about/update": () => {
            posted = true; // the answer is lost, the update runs
            return Promise.reject(new TypeError("Failed to fetch"));
          },
        },
        { onBack },
      );
      expect(await screen.findByText("edelweiss is restarting …")).toBeTruthy();
      await waitFor(() => expect(onBack).toHaveBeenCalledTimes(1));
      expect(document.body.textContent).not.toContain("Failed to fetch");
    });

    it("ends the overlay when the helper reports a failed update without a restart", async () => {
      healthAnswers("t1");
      const onBack = vi.fn();
      let posted = false;
      const failed = {
        from: "0.3.1",
        to: "0.4.0",
        outcome: "failed",
        reason: "swap_failed",
        at: "2026-11-05T11:00:00Z",
      };
      const v = await confirmed(
        {
          "POST /api/about/update": () => {
            posted = true;
            return json(202, { to: "0.4.0" });
          },
          "GET /api/about": () =>
            json(
              200,
              info({ state: posted ? { running: false, result: failed as never } : { running: false } }),
            ),
        },
        { onBack },
      );
      expect(await v.findByText(/update from 0\.3\.1 to 0\.4\.0 failed/)).toBeTruthy();
      expect(screen.queryByText("edelweiss is restarting …")).toBeNull();
      expect(v.container.hasAttribute("inert")).toBe(false);
      expect(onBack).not.toHaveBeenCalled();
    });

    // A session lives in the memory of the process: an answer from info() can only come from the old edelweiss, so no
    // restart happened, whatever the helper did or did not write.
    it("ends the overlay with the result of a rollback while the old edelweiss never stopped", async () => {
      healthAnswers("t1");
      const onBack = vi.fn();
      const rolledBack = {
        from: "0.3.1",
        to: "0.4.0",
        outcome: "rolled_back",
        reason: "restart_failed",
        at: "2026-11-05T11:00:00Z",
      };
      const v = await confirmed(
        {
          "POST /api/about/update": () => {
            posted = true;
            return json(202, { to: "0.4.0" });
          },
          "GET /api/about": () =>
            json(
              200,
              info({ state: { running: false, ...(posted ? { result: rolledBack as never } : {}) } }),
            ),
        },
        { onBack },
      );
      expect(await v.findByText(/update from 0\.3\.1 to 0\.4\.0 was rolled back/)).toBeTruthy();
      expect(screen.queryByText("edelweiss is restarting …")).toBeNull();
      expect(v.container.hasAttribute("inert")).toBe(false);
      expect(onBack).not.toHaveBeenCalled();
    });

    it("says that the update did not start when the helper ended without a result", async () => {
      healthAnswers("t1");
      const onBack = vi.fn();
      const v = await confirmed({ "GET /api/about": () => json(200, info()) }, { onBack, timeoutMs: 400 });
      expect(await v.findByText("The update did not start.")).toBeTruthy();
      expect(screen.queryByText("edelweiss is restarting …")).toBeNull();
      expect(v.container.hasAttribute("inert")).toBe(false);
      expect(onBack).not.toHaveBeenCalled();
    });

    it("says that the update did not start when an older result is all there is, as when the start never arrived", async () => {
      healthAnswers("t1");
      const old = { from: "0.3.0", to: "0.3.1", outcome: "failed", at: "2026-10-01T11:00:00Z" };
      const v = await confirmed(
        {
          "POST /api/about/update": () => Promise.reject(new TypeError("Failed to fetch")),
          "GET /api/about": () => json(200, info({ state: { running: false, result: old as never } })),
        },
        { timeoutMs: 400 },
      );
      expect(await v.findByText("The update did not start.")).toBeTruthy();
      expect(screen.queryByText("edelweiss is restarting …")).toBeNull();
      expect(v.container.hasAttribute("inert")).toBe(false);
    });

    it("closes the dialog when the refusal removes the offer, keeps the refusal on the page and does not reopen", async () => {
      healthAnswers("t1");
      let refreshed = 0;
      const v = await confirmed({
        "POST /api/about/update": () => json(409, { error: { code: "release_changed", message: "changed" } }),
        "GET /api/about/update?refresh=1": () => {
          refreshed++;
          return json(200, refreshed === 1 ? view() : offer());
        },
      });
      await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
      const note = await v.findByText(/release changed after you reviewed it/);
      expect(note.closest("[role=alert]")).not.toBeNull();
      expect(v.container.hasAttribute("inert")).toBe(false);
      fireEvent.click(await v.findByRole("button", { name: "Check again" }));
      await waitFor(() => expect(refreshed).toBe(2));
      await v.findByRole("button", { name: "Update" });
      await new Promise((r) => setTimeout(r, 30));
      expect(screen.queryByRole("dialog")).toBeNull();
    });

    it("does not let a hanging health check hold the start back", async () => {
      let asked = 0;
      vi.stubGlobal(
        "fetch",
        vi.fn(async (_url: string, init?: RequestInit) => {
          asked++;
          if (asked === 1) {
            return new Promise<Response>((_resolve, reject) => {
              init?.signal?.addEventListener("abort", () => reject(new DOMException("t", "TimeoutError")));
            });
          }
          if (asked === 2) throw new TypeError("down");
          return new Response(JSON.stringify({ status: "ok", version: "x", started: "t2" }), { status: 200 });
        }),
      );
      const onBack = vi.fn();
      const v = await confirmed({}, { onBack, probeMs: 30 });
      await waitFor(() => expect(onBack).toHaveBeenCalledTimes(1));
      expect(v.fetch.calls.some((c) => c.method === "POST" && c.url === "/api/about/update")).toBe(true);
    });

    it("makes the page inert while the dialog is open and gives the focus back to the Update button", async () => {
      const v = await open(inAdmin(info(), offer()));
      const button = await v.findByRole("button", { name: "Update" });
      fireEvent.click(button);
      const dialog = await screen.findByRole("dialog", { name: "Update edelweiss" });
      expect(v.container.hasAttribute("inert")).toBe(true);
      fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
      await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
      expect(v.container.hasAttribute("inert")).toBe(false);
      await waitFor(() => expect(document.activeElement).toBe(button));
    });

    it("waits five minutes by default", async () => {
      const clock = { now: 1_000 };
      vi.spyOn(Date, "now").mockImplementation(() => clock.now);
      healthAnswers("t1");
      await confirmed({}, { pollMs: 10 });
      await screen.findByText("edelweiss is restarting …");
      clock.now += 299_000;
      await new Promise((r) => setTimeout(r, 60));
      expect(screen.queryByText(/edelweiss does not answer/)).toBeNull();
      clock.now += 2_000;
      expect(await screen.findByText(/edelweiss does not answer/)).toBeTruthy();
    });
  });
});
