import type { ExtensionInfo } from "@edelweiss/sdk";
import { sessionInfo } from "@edelweiss/sdk/testing";
import { cleanup, fireEvent, waitFor, within } from "@solidjs/testing-library";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { json } from "../test/fake-fetch";
import { ext, noUpdates } from "../test/fixtures";
import { mount, remount, settle } from "../test/mount";
import { ExtensionsPage } from "./Extensions";

beforeAll(() => {
  vi.spyOn(window, "scrollTo").mockImplementation(() => {});
});
afterEach(cleanup);

const SUM = "ab".repeat(32);
const INSTALLED_SUM = "ef".repeat(32);
const NEWER_SUM = "cd".repeat(32);

function hello(version: string): ExtensionInfo {
  return {
    ...ext("hello", "tools", 90, "Hello"),
    version,
    source: {
      type: "github",
      location: "github.com/edelweiss/hello",
      repo: "edelweiss/hello",
      tag: `v${version}`,
      asset: `edelweiss-ext-hello_${version}_linux_amd64`,
      sha256: INSTALLED_SUM,
      installedAt: "2026-10-08T00:00:00Z",
    },
  };
}

const resolution = {
  repo: "edelweiss/hello",
  tag: "v0.1.0",
  name: "v0.1.0",
  url: "https://github.com/edelweiss/hello/releases/tag/v0.1.0",
  publishedAt: "2026-10-01T12:00:00Z",
  candidates: [
    {
      id: "hello",
      version: "0.1.0",
      arch: "amd64",
      asset: "edelweiss-ext-hello_0.1.0_linux_amd64",
      size: 2048,
      sha256: SUM,
      archive: false,
    },
  ],
};

/** What GET /api/extensions/updates lists for hello 0.1.0. */
const UPDATE = {
  id: "hello",
  installed: "0.1.0",
  available: "0.2.0",
  repo: "edelweiss/hello",
  tag: "v0.2.0",
  asset: "edelweiss-ext-hello_0.2.0_linux_amd64.tar.gz",
  sha256: SUM,
};
const offerUpdate = () =>
  json(200, {
    updates: [UPDATE],
    errors: [],
    checkedAt: new Date().toISOString(),
    ageSeconds: 0,
    stale: false,
  });

const admin = () => json(200, sessionInfo({ admin: true }));
const leaveAdmin = () => json(200, sessionInfo({ admin: false }));

/** A promise settled from outside, to hold an answer of the fake server back. */
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((r) => {
    resolve = r;
  });
  return { promise, resolve };
}

async function open(routes: Parameters<typeof mount>[0]) {
  const view = mount(routes, () => <ExtensionsPage />);
  await view.shell.session.load();
  await view.shell.extensions.reload();
  await settle();
  return view;
}

type View = Awaited<ReturnType<typeof open>>;
const toasts = (view: View) => view.shell.toasts.items().map((t) => t.message);
const backdrop = (view: View) => view.container.querySelector(".modal-backdrop")!;

describe("extensions page: list", () => {
  it("says where an extension came from", async () => {
    const withoutTag: ExtensionInfo = {
      ...ext("bare", "tools", 91, "Bare"),
      source: {
        type: "github",
        location: "github.com/acme/bare",
        repo: "acme/bare",
        sha256: "abc",
        installedAt: "2026-10-08T00:00:00Z",
      },
    };
    const view = await open({
      "GET /api/session": () => json(200, sessionInfo({ canAdmin: false })),
      "GET /api/extensions": () =>
        json(200, { extensions: [hello("0.1.0"), withoutTag, ext("plain", "tools", 92, "Plain")] }),
    });
    expect(view.getByText("github: edelweiss/hello @ v0.1.0")).toBeTruthy();
    expect(view.getByText("github: acme/bare")).toBeTruthy();
    expect(view.getByText("file: /tmp/plain")).toBeTruthy();
  });

  it("warns about missing packages with the command that installs them", async () => {
    const docker: ExtensionInfo = {
      ...ext("docker", "virtualization", 10, "Docker"),
      requires: { apk: ["docker"] },
      missingPackages: ["docker"],
    };
    const view = await open({
      "GET /api/session": () => json(200, sessionInfo({ canAdmin: false })),
      "GET /api/extensions": () => json(200, { extensions: [docker, ext("plain", "tools", 92, "Plain")] }),
    });
    expect(view.getByText(/Missing packages: docker\. Install them as root:/)).toBeTruthy();
    expect(view.getByText("apk add docker")).toBeTruthy();
    expect(view.getAllByText(/Missing packages/)).toHaveLength(1);
    // a warning box with its own readable colours; text in the plain warning colour is unreadable on the light theme
    const warning = view.getByText(/Missing packages: docker/).closest(".alert-warning");
    expect(warning?.textContent).toContain("apk add docker");
    expect(view.container.querySelector(".text-warning")).toBeNull();
  });

  it("offers the removal as a trash icon with a tooltip and the extension's name for assistive technology", async () => {
    const view = await open({
      "GET /api/session": admin,
      "GET /api/extensions": () => json(200, { extensions: [ext("docker", "virtualization", 10, "Docker")] }),
      "GET /api/extensions/updates": noUpdates,
    });
    const remove = view.getByRole("button", { name: "Remove Docker" }) as HTMLButtonElement;
    expect(remove.disabled).toBe(false);
    // text-error beats daisyUI's disabled colour, so the disabled state needs its own look
    expect(remove.classList.contains("disabled:opacity-40")).toBe(true);
    const icon = remove.querySelector("svg");
    expect(icon).not.toBeNull();
    expect(icon?.getAttribute("width")).toBe("18");
    expect(icon?.getAttribute("stroke-width")).toBe("1.75");
    expect(remove.textContent?.trim()).toBe("");
    expect(remove.parentElement?.classList.contains("tooltip")).toBe(true);
    expect(remove.parentElement?.getAttribute("data-tip")).toBe("Remove");
    // the bubble opens to the left and does not wrap: centred above the button in the last cell it would reach beyond
    // the scroll box of the table (an invisible bubble still counts) and show a horizontal scrollbar
    for (const cls of ["tooltip-left", "before:max-w-none", "before:whitespace-nowrap"]) {
      expect(remove.parentElement?.classList.contains(cls), cls).toBe(true);
    }
  });
});

describe("extensions page: install from GitHub", () => {
  it("installs the reviewed release and lists the extension", async () => {
    let installed = false;
    const view = await open({
      "GET /api/session": admin,
      "GET /api/extensions": () => json(200, { extensions: installed ? [hello("0.1.0")] : [] }),
      "GET /api/extensions/updates": noUpdates,
      "POST /api/extensions/resolve": () => json(200, resolution),
      "POST /api/extensions/install": () => {
        installed = true;
        return json(201, hello("0.1.0"));
      },
    });
    fireEvent.click(view.getByRole("button", { name: "Install from GitHub" }));
    const dialog = await view.findByRole("dialog", { name: "Install from GitHub" });
    const input = view.getByLabelText("Repository") as HTMLInputElement;
    expect(document.activeElement).toBe(input);
    fireEvent.input(input, { target: { value: " edelweiss/hello " } });
    fireEvent.submit(dialog.querySelector("form")!);

    expect(await view.findByText(/edelweiss-ext-hello_0.1.0_linux_amd64/)).toBeTruthy();
    expect(view.getByText(new RegExp(SUM))).toBeTruthy();
    expect(view.getByText(/Only install extensions from sources you trust/)).toBeTruthy();
    fireEvent.click(view.getByRole("button", { name: "Install" }));

    await waitFor(() => expect(view.queryByRole("dialog")).toBeNull());
    const resolve = view.fetch.calls.find((c) => c.url === "/api/extensions/resolve");
    const install = view.fetch.calls.find((c) => c.url === "/api/extensions/install");
    expect(resolve?.body).toEqual({ source: "edelweiss/hello" });
    expect(install?.body).toEqual({ source: "edelweiss/hello@v0.1.0", id: "hello", sha256: SUM });
    expect(toasts(view)).toContain("Hello 0.1.0 was installed.");
    expect(await view.findByText("github: edelweiss/hello @ v0.1.0")).toBeTruthy();
  });

  it("checks for updates after an install: a just-installed older release offers its update at once", async () => {
    let installed = false;
    let checks = 0;
    const view = await open({
      "GET /api/session": admin,
      "GET /api/extensions": () => json(200, { extensions: installed ? [hello("0.1.0")] : [] }),
      "GET /api/extensions/updates": () => {
        checks++;
        return installed ? offerUpdate() : noUpdates();
      },
      "POST /api/extensions/resolve": () => json(200, resolution),
      "POST /api/extensions/install": () => {
        installed = true;
        return json(201, hello("0.1.0"));
      },
    });
    expect(checks).toBe(1); // the check that admin mode triggers
    expect(view.queryByRole("button", { name: "Update to 0.2.0" })).toBeNull();
    fireEvent.click(view.getByRole("button", { name: "Install from GitHub" }));
    const dialog = await view.findByRole("dialog", { name: "Install from GitHub" });
    fireEvent.input(view.getByLabelText("Repository"), { target: { value: "edelweiss/hello" } });
    fireEvent.submit(dialog.querySelector("form")!);
    fireEvent.click(await view.findByRole("button", { name: "Install" }));

    await waitFor(() => expect(view.queryByRole("dialog")).toBeNull());
    expect(await view.findByRole("button", { name: "Update to 0.2.0" })).toBeTruthy();
    expect(checks).toBe(2);
  });

  it("shows what went wrong and stays open", async () => {
    const view = await open({
      "GET /api/session": admin,
      "GET /api/extensions": () => json(200, { extensions: [] }),
      "GET /api/extensions/updates": noUpdates,
      "POST /api/extensions/resolve": (req) =>
        (req.body as { source: string }).source === "nope/nope"
          ? json(404, { error: { code: "release_not_found", message: "nope/nope has no release" } })
          : json(200, resolution),
      "POST /api/extensions/install": () =>
        json(422, { error: { code: "checksum_mismatch", message: "the download does not match" } }),
    });
    fireEvent.click(view.getByRole("button", { name: "Install from GitHub" }));
    const dialog = await view.findByRole("dialog", { name: "Install from GitHub" });
    fireEvent.input(view.getByLabelText("Repository"), { target: { value: "nope/nope" } });
    fireEvent.submit(dialog.querySelector("form")!);
    expect((await view.findByRole("alert")).textContent).toBe(
      "No release was found for this repository or tag.",
    );

    fireEvent.input(view.getByLabelText("Repository"), { target: { value: "edelweiss/hello" } });
    fireEvent.submit(dialog.querySelector("form")!);
    fireEvent.click(await view.findByRole("button", { name: "Install" }));
    await waitFor(() =>
      expect(view.getByText("The download does not match its checksum. Nothing was installed.")).toBeTruthy(),
    );
    expect(view.getByRole("dialog", { name: "Install from GitHub" })).toBeTruthy();

    fireEvent.keyDown(document, { key: "Escape" });
    await waitFor(() => expect(view.queryByRole("dialog")).toBeNull());
  });

  it("names the missing packages and the command that installs them", async () => {
    const view = await open({
      "GET /api/session": admin,
      "GET /api/extensions": () => json(200, { extensions: [] }),
      "GET /api/extensions/updates": noUpdates,
      "POST /api/extensions/resolve": () => json(200, resolution),
      "POST /api/extensions/install": () =>
        json(422, {
          error: {
            code: "missing_packages",
            message: "missing packages: docker, cmd:docker-compose",
            packages: ["docker", "cmd:docker-compose"],
          },
        }),
    });
    fireEvent.click(view.getByRole("button", { name: "Install from GitHub" }));
    const dialog = await view.findByRole("dialog", { name: "Install from GitHub" });
    fireEvent.input(view.getByLabelText("Repository"), { target: { value: "edelweiss/hello" } });
    fireEvent.submit(dialog.querySelector("form")!);
    fireEvent.click(await view.findByRole("button", { name: "Install" }));
    await waitFor(() =>
      expect(
        view.getByText(
          'This extension needs packages that are not installed: docker, cmd:docker-compose. Install them as root with "apk add docker cmd:docker-compose" and try again. Nothing was changed.',
        ),
      ).toBeTruthy(),
    );
    expect(view.getByRole("dialog", { name: "Install from GitHub" })).toBeTruthy();
  });

  it("keeps the dialog and the choice while the install runs, even when admin mode ends", async () => {
    const answer = deferred<Response>();
    const view = await open({
      "GET /api/session": admin,
      "GET /api/extensions": () => json(200, { extensions: [] }),
      "GET /api/extensions/updates": noUpdates,
      "POST /api/extensions/resolve": () => json(200, resolution),
      "POST /api/extensions/install": () => answer.promise,
      "DELETE /api/session/admin": leaveAdmin,
    });
    fireEvent.click(view.getByRole("button", { name: "Install from GitHub" }));
    const dialog = await view.findByRole("dialog", { name: "Install from GitHub" });
    fireEvent.input(view.getByLabelText("Repository"), { target: { value: "edelweiss/hello" } });
    fireEvent.submit(dialog.querySelector("form")!);
    fireEvent.click(await view.findByRole("button", { name: "Install" }));

    // the file under review cannot be swapped while it is being installed
    const choice = dialog.querySelector("fieldset")!;
    await waitFor(() => expect(choice.disabled).toBe(true));
    expect(choice.querySelector("input")!.matches(":disabled")).toBe(true);
    fireEvent.keyDown(document, { key: "Escape" });
    fireEvent.click(backdrop(view));
    await view.shell.session.disableAdmin();
    await settle();
    expect(view.getByRole("dialog", { name: "Install from GitHub" })).toBeTruthy();

    answer.resolve(json(201, hello("0.1.0")));
    await waitFor(() => expect(view.queryByRole("dialog")).toBeNull());
    expect(toasts(view)).toContain("Hello 0.1.0 was installed.");
    // update checks need admin mode: the one that turning it on triggered is the only one, none after the install
    await settle();
    expect(view.fetch.calls.filter((c) => c.url === "/api/extensions/updates")).toHaveLength(1);
  });

  it("closes the dialog when admin mode ends", async () => {
    const view = await open({
      "GET /api/session": admin,
      "GET /api/extensions": () => json(200, { extensions: [] }),
      "GET /api/extensions/updates": noUpdates,
      "DELETE /api/session/admin": leaveAdmin,
    });
    fireEvent.click(view.getByRole("button", { name: "Install from GitHub" }));
    await view.findByRole("dialog", { name: "Install from GitHub" });
    await view.shell.session.disableAdmin();
    await waitFor(() => expect(view.queryByRole("dialog")).toBeNull());
  });

  it("asks for admin mode first and does nothing when it is declined", async () => {
    const view = await open({
      "GET /api/session": () => json(200, sessionInfo({ admin: false, canAdmin: true })),
      "GET /api/extensions": () => json(200, { extensions: [] }),
    });
    fireEvent.click(view.getByRole("button", { name: "Install from GitHub" }));
    await waitFor(() => expect(view.shell.session.adminRequested()).toBe(true));
    view.shell.session.answerAdminRequest(false);
    await settle();
    expect(view.queryByRole("dialog", { name: "Install from GitHub" })).toBeNull();
    expect(view.fetch.calls.some((c) => c.url.startsWith("/api/extensions/"))).toBe(false);
  });

  it("is not offered to users who may not use admin mode", async () => {
    const view = await open({
      "GET /api/session": () => json(200, sessionInfo({ admin: false, canAdmin: false })),
      "GET /api/extensions": () => json(200, { extensions: [] }),
    });
    expect(view.queryByRole("button", { name: "Install from GitHub" })).toBeNull();
    expect(view.queryByRole("button", { name: "Check for updates" })).toBeNull();
  });
});

describe("extensions page: updates", () => {
  it("checks in admin mode, updates after confirmation and checks again", async () => {
    let version = "0.1.0";
    let checks = 0;
    const view = await open({
      "GET /api/session": admin,
      "GET /api/extensions": () => json(200, { extensions: [hello(version)] }),
      "GET /api/extensions/updates": () => {
        checks++;
        return version === "0.1.0" ? offerUpdate() : noUpdates();
      },
      "POST /api/extensions/hello/update": () => {
        version = "0.2.0";
        return json(200, hello("0.2.0"));
      },
    });
    fireEvent.click(await view.findByRole("button", { name: "Update to 0.2.0" }));
    const confirm = await view.findByRole("dialog", { name: "Update extension" });
    expect(confirm.textContent).toContain("Update Hello from 0.1.0 to 0.2.0?");
    // what the user confirms: the release, the file and its checksum, with the same warning as for an install
    expect(confirm.textContent).toContain("edelweiss/hello");
    expect(confirm.textContent).toContain("v0.2.0");
    expect(confirm.textContent).toContain("0.1.0 → 0.2.0");
    expect(confirm.textContent).toContain("edelweiss-ext-hello_0.2.0_linux_amd64.tar.gz");
    expect(confirm.textContent).toContain(SUM);
    expect(confirm.textContent).toContain("Only install extensions from sources you trust");
    expect(confirm.querySelector('[role="alert"]')).toBeNull();
    expect(document.activeElement).toBe(view.getByRole("button", { name: "Cancel" }));

    fireEvent.click(view.getByRole("button", { name: "Update" }));
    await waitFor(() => expect(toasts(view)).toContain("Hello was updated to 0.2.0."));
    await waitFor(() => expect(view.queryByRole("button", { name: "Update to 0.2.0" })).toBeNull());
    expect(view.queryByRole("dialog")).toBeNull();
    expect(checks).toBe(2);
    // the update carries the checksum of the release the user confirmed
    const update = view.fetch.calls.find((c) => c.url === "/api/extensions/hello/update");
    expect(update?.body).toEqual({ sha256: SUM });
  });

  const waysToCancel: [string, (view: View) => void][] = [
    ["the cancel button", (view) => fireEvent.click(view.getByRole("button", { name: "Cancel" }))],
    ["the escape key", () => fireEvent.keyDown(document, { key: "Escape" })],
    ["a click on the backdrop", (view) => fireEvent.click(backdrop(view))],
  ];
  it.each(waysToCancel)(
    "does not update when the confirmation is cancelled with %s",
    async (_name, cancel) => {
      const view = await open({
        "GET /api/session": admin,
        "GET /api/extensions": () => json(200, { extensions: [hello("0.1.0")] }),
        "GET /api/extensions/updates": offerUpdate,
      });
      fireEvent.click(await view.findByRole("button", { name: "Update to 0.2.0" }));
      await view.findByRole("dialog", { name: "Update extension" });
      cancel(view);
      expect(view.queryByRole("dialog")).toBeNull();
      expect(view.fetch.calls.some((c) => c.url === "/api/extensions/hello/update")).toBe(false);
      expect(view.getByRole("button", { name: "Update to 0.2.0" })).toBeTruthy();
    },
  );

  it("keeps the dialog open and the row locked while the update runs", async () => {
    const answer = deferred<Response>();
    let version = "0.1.0";
    const view = await open({
      "GET /api/session": admin,
      "GET /api/extensions": () => json(200, { extensions: [hello(version)] }),
      "GET /api/extensions/updates": () => (version === "0.1.0" ? offerUpdate() : noUpdates()),
      "POST /api/extensions/hello/update": () => answer.promise,
    });
    fireEvent.click(await view.findByRole("button", { name: "Update to 0.2.0" }));
    await view.findByRole("dialog", { name: "Update extension" });
    fireEvent.click(view.getByRole("button", { name: "Update" }));

    const button = (name: string) => view.getByRole("button", { name }) as HTMLButtonElement;
    await waitFor(() => expect(button("Remove Hello").disabled).toBe(true));
    expect(button("Update to 0.2.0").disabled).toBe(true);
    expect(button("Update").disabled).toBe(true);
    expect(button("Update").querySelector(".loading")).not.toBeNull();
    expect(button("Cancel").disabled).toBe(true);
    // neither Escape nor the backdrop get the user out of an update that is running
    fireEvent.keyDown(document, { key: "Escape" });
    fireEvent.click(backdrop(view));
    await settle();
    expect(view.getByRole("dialog", { name: "Update extension" })).toBeTruthy();
    expect(toasts(view)).toEqual([]);

    version = "0.2.0";
    answer.resolve(json(200, hello("0.2.0")));
    await waitFor(() => expect(toasts(view)).toEqual(["Hello was updated to 0.2.0."]));
    expect(view.queryByRole("dialog")).toBeNull();
    await waitFor(() => expect(button("Remove Hello").disabled).toBe(false));
  });

  it("closes the confirmation when admin mode ends", async () => {
    const view = await open({
      "GET /api/session": admin,
      "GET /api/extensions": () => json(200, { extensions: [hello("0.1.0")] }),
      "GET /api/extensions/updates": offerUpdate,
      "DELETE /api/session/admin": leaveAdmin,
    });
    fireEvent.click(await view.findByRole("button", { name: "Update to 0.2.0" }));
    await view.findByRole("dialog", { name: "Update extension" });
    await view.shell.session.disableAdmin();
    await waitFor(() => expect(view.queryByRole("dialog")).toBeNull());
    expect(view.queryByRole("button", { name: /^Update to/ })).toBeNull();
  });

  it("reports an update the core refuses because the release changed and offers the current one", async () => {
    // the core keeps its last check for 2 minutes: only a check with refresh=1 sees the release as it is now
    const current = () => {
      const newer = { ...UPDATE, available: "0.3.0", tag: "v0.3.0", sha256: NEWER_SUM };
      return json(200, {
        updates: [newer],
        errors: [],
        checkedAt: new Date().toISOString(),
        ageSeconds: 0,
        stale: false,
      });
    };
    const view = await open({
      "GET /api/session": admin,
      "GET /api/extensions": () => json(200, { extensions: [hello("0.1.0")] }),
      "GET /api/extensions/updates": offerUpdate, // the kept check, with the old checksum
      "GET /api/extensions/updates?refresh=1": current,
      "POST /api/extensions/hello/update": () =>
        json(409, { error: { code: "release_changed", message: "the release has another checksum" } }),
    });
    fireEvent.click(await view.findByRole("button", { name: "Update to 0.2.0" }));
    const dialog = await view.findByRole("dialog", { name: "Update extension" });
    fireEvent.click(view.getByRole("button", { name: "Update" }));
    // the refusal is told in the dialog, where it stays until the user has read it; no toast
    expect((await within(dialog).findByRole("alert")).textContent).toBe(
      "The release changed after you reviewed it. Look it up again.",
    );
    expect(toasts(view)).toEqual([]);
    expect(view.shell.extensions.list().map((e) => e.version)).toEqual(["0.1.0"]);

    // the entry with the old checksum is gone: the release as it is now is offered, found out on GitHub
    expect(await view.findByRole("button", { name: "Update to 0.3.0" })).toBeTruthy();
    expect(view.queryByRole("button", { name: "Update to 0.2.0" })).toBeNull();
    const checks = view.fetch.calls.filter((c) => c.url.startsWith("/api/extensions/updates"));
    expect(checks.map((c) => c.url)).toEqual([
      "/api/extensions/updates",
      "/api/extensions/updates?refresh=1",
    ]);
  });

  it("keeps the dialog open and names the missing packages when the core refuses an update for them", async () => {
    const view = await open({
      "GET /api/session": admin,
      "GET /api/extensions": () => json(200, { extensions: [hello("0.1.0")] }),
      "GET /api/extensions/updates": offerUpdate,
      "GET /api/extensions/updates?refresh=1": offerUpdate,
      "POST /api/extensions/hello/update": () =>
        json(422, {
          error: { code: "missing_packages", message: "missing packages: docker", packages: ["docker"] },
        }),
    });
    fireEvent.click(await view.findByRole("button", { name: "Update to 0.2.0" }));
    const dialog = await view.findByRole("dialog", { name: "Update extension" });
    fireEvent.click(view.getByRole("button", { name: "Update" }));

    const refusal = await within(dialog).findByRole("alert");
    expect(refusal.textContent).toBe(
      'This extension needs packages that are not installed: docker. Install them as root with "apk add docker" and try again. Nothing was changed.',
    );
    expect(refusal.classList.contains("alert-error")).toBe(true);
    expect(view.getByRole("dialog", { name: "Update extension" })).toBeTruthy();
    expect(toasts(view)).toEqual([]);
    // the user can read it, cancel, or try again after installing the packages
    expect((view.getByRole("button", { name: "Update" }) as HTMLButtonElement).disabled).toBe(false);
    expect((view.getByRole("button", { name: "Cancel" }) as HTMLButtonElement).disabled).toBe(false);
    expect(view.shell.extensions.list().map((e) => e.version)).toEqual(["0.1.0"]);
    // the update is still on offer
    expect(view.getByRole("button", { name: "Update to 0.2.0" })).toBeTruthy();
  });

  describe("after a refused update", () => {
    const refuse = () =>
      json(422, {
        error: { code: "missing_packages", message: "missing packages: docker", packages: ["docker"] },
      });

    it("does not show the refusal again when the dialog is opened again", async () => {
      const view = await open({
        "GET /api/session": admin,
        "GET /api/extensions": () => json(200, { extensions: [hello("0.1.0")] }),
        "GET /api/extensions/updates": offerUpdate,
        "GET /api/extensions/updates?refresh=1": offerUpdate,
        "POST /api/extensions/hello/update": refuse,
      });
      fireEvent.click(await view.findByRole("button", { name: "Update to 0.2.0" }));
      const dialog = await view.findByRole("dialog", { name: "Update extension" });
      fireEvent.click(view.getByRole("button", { name: "Update" }));
      await within(dialog).findByRole("alert");

      fireEvent.click(view.getByRole("button", { name: "Cancel" }));
      expect(view.queryByRole("dialog")).toBeNull();
      fireEvent.click(view.getByRole("button", { name: "Update to 0.2.0" }));
      const again = await view.findByRole("dialog", { name: "Update extension" });
      expect(within(again).queryByRole("alert")).toBeNull();
    });

    it("clears the refusal when the update is confirmed again and closes the dialog once it succeeds", async () => {
      const answer = deferred<Response>();
      let attempts = 0;
      let version = "0.1.0";
      const view = await open({
        "GET /api/session": admin,
        "GET /api/extensions": () => json(200, { extensions: [hello(version)] }),
        "GET /api/extensions/updates": () => (version === "0.1.0" ? offerUpdate() : noUpdates()),
        "GET /api/extensions/updates?refresh=1": () => (version === "0.1.0" ? offerUpdate() : noUpdates()),
        "POST /api/extensions/hello/update": () => (++attempts === 1 ? refuse() : answer.promise),
      });
      fireEvent.click(await view.findByRole("button", { name: "Update to 0.2.0" }));
      const dialog = await view.findByRole("dialog", { name: "Update extension" });
      fireEvent.click(view.getByRole("button", { name: "Update" }));
      await within(dialog).findByRole("alert");

      fireEvent.click(view.getByRole("button", { name: "Update" }));
      await waitFor(() => expect(within(dialog).queryByRole("alert")).toBeNull());
      expect((view.getByRole("button", { name: "Update" }) as HTMLButtonElement).disabled).toBe(true);

      version = "0.2.0";
      answer.resolve(json(200, hello("0.2.0")));
      await waitFor(() => expect(toasts(view)).toEqual(["Hello was updated to 0.2.0."]));
      expect(view.queryByRole("dialog")).toBeNull();
    });

    it("closes the dialog with the refusal when admin mode ends", async () => {
      const view = await open({
        "GET /api/session": admin,
        "GET /api/extensions": () => json(200, { extensions: [hello("0.1.0")] }),
        "GET /api/extensions/updates": offerUpdate,
        "GET /api/extensions/updates?refresh=1": offerUpdate,
        "POST /api/extensions/hello/update": refuse,
        "DELETE /api/session/admin": leaveAdmin,
      });
      fireEvent.click(await view.findByRole("button", { name: "Update to 0.2.0" }));
      const dialog = await view.findByRole("dialog", { name: "Update extension" });
      fireEvent.click(view.getByRole("button", { name: "Update" }));
      await within(dialog).findByRole("alert");

      await view.shell.session.disableAdmin();
      await waitFor(() => expect(view.queryByRole("dialog")).toBeNull());
      expect(view.queryByRole("alert")).toBeNull();
    });
  });

  it("ignores the answer of a check that admin mode did not outlive", async () => {
    const answer = deferred<Response>();
    const view = await open({
      "GET /api/session": admin,
      "GET /api/extensions": () => json(200, { extensions: [hello("0.1.0")] }),
      "GET /api/extensions/updates": () => answer.promise,
      "DELETE /api/session/admin": leaveAdmin,
    });
    expect(view.getByRole("button", { name: "Checking…" })).toBeTruthy();
    await view.shell.session.disableAdmin();
    await settle();

    answer.resolve(
      json(200, {
        updates: [UPDATE],
        errors: [{ id: "docker", code: "release_not_found", message: "acme/docker has no release" }],
      }),
    );
    await settle();
    expect(view.queryByRole("button", { name: /^Update to/ })).toBeNull();
    expect(toasts(view)).toEqual([]);
    expect((view.getByRole("button", { name: "Check for updates" }) as HTMLButtonElement).disabled).toBe(
      false,
    );
  });

  it("does not check without admin mode until asked", async () => {
    const view = await open({
      "GET /api/session": () => json(200, sessionInfo({ admin: false, canAdmin: true })),
      "GET /api/extensions": () => json(200, { extensions: [hello("0.1.0")] }),
    });
    expect(view.fetch.calls.some((c) => c.url === "/api/extensions/updates")).toBe(false);
    fireEvent.click(view.getByRole("button", { name: "Check for updates" }));
    await waitFor(() => expect(view.shell.session.adminRequested()).toBe(true));
  });

  it("reports extensions whose check failed", async () => {
    const view = await open({
      "GET /api/session": admin,
      "GET /api/extensions": () => json(200, { extensions: [hello("0.1.0")] }),
      "GET /api/extensions/updates": () =>
        json(200, {
          updates: [],
          errors: [{ id: "docker", code: "release_not_found", message: "acme/docker has no release" }],
        }),
    });
    await waitFor(() =>
      expect(toasts(view)).toContain(
        "Could not check docker for updates: No release was found for this repository or tag.",
      ),
    );
  });
});

describe("extensions page: the update check keeps its result", () => {
  const NOW = new Date("2026-10-12T10:00:00Z");
  const secondsAgo = (n: number) => new Date(NOW.getTime() - n * 1000);
  const minutesAgo = (n: number) => secondsAgo(n * 60);
  // how the stale note names the time of the kept check: date and time, in the language of the shell
  const stamp = (d: Date, locale = "en") =>
    new Intl.DateTimeFormat(locale, { dateStyle: "short", timeStyle: "short" }).format(d);
  const extensions = () => json(200, { extensions: [hello("0.1.0")] });
  /** The requests the page made for the update check, with their query. */
  const checkCalls = (view: View) =>
    view.fetch.calls.filter((c) => c.url.startsWith("/api/extensions/updates")).map((c) => c.url);
  /** The core's answer to a check that was made at checkedAt; the core says how old it is by its own clock (NOW). */
  const checkedAt = (at: Date, extra: Record<string, unknown> = {}) =>
    json(200, {
      updates: [],
      errors: [],
      checkedAt: at.toISOString(),
      ageSeconds: Math.max(0, Math.round((NOW.getTime() - at.getTime()) / 1000)),
      stale: false,
      ...extra,
    });
  const staleAnswer = (staleCode = "github_rate_limited", staleReason = "GitHub API rate limit exceeded") =>
    checkedAt(minutesAgo(7), { updates: [UPDATE], stale: true, staleCode, staleReason });
  const hint = (view: View) => view.container.querySelector(".alert-warning");
  const errorOfDocker = { id: "docker", code: "release_not_found", message: "acme/docker has no release" };
  const toastOfDocker =
    "Could not check docker for updates: No release was found for this repository or tag.";

  /** Runs test with the clock of the browser (Date.now) standing at the given time, by default at NOW. */
  async function atNow(test: () => Promise<void>, browserTime = NOW.getTime()) {
    const clock = vi.spyOn(Date, "now").mockReturnValue(browserTime);
    try {
      await test();
    } finally {
      clock.mockRestore();
    }
  }

  it("asks the core without refresh when it opens and with refresh when the button is pressed", async () => {
    const view = await open({
      "GET /api/session": admin,
      "GET /api/extensions": extensions,
      "GET /api/extensions/updates": noUpdates,
      "GET /api/extensions/updates?refresh=1": noUpdates,
    });
    expect(checkCalls(view)).toEqual(["/api/extensions/updates"]);
    fireEvent.click(view.getByRole("button", { name: "Check for updates" }));
    await waitFor(() =>
      expect(checkCalls(view)).toEqual(["/api/extensions/updates", "/api/extensions/updates?refresh=1"]),
    );
  });

  const HOUR = 3600;
  const DAY = 24 * HOUR;
  it.each([
    [0, "Checked just now"],
    [59, "Checked just now"],
    [60, "Checked 1 minute ago"],
    [3 * 60, "Checked 3 minutes ago"],
    [59 * 60 + 59, "Checked 59 minutes ago"],
    // from an hour on the page counts in hours, from a day on in days (the last good result can be that old)
    [HOUR, "Checked 1 hour ago"],
    [75 * 60, "Checked 1 hour ago"],
    [5 * HOUR + 1800, "Checked 5 hours ago"],
    [DAY - 1, "Checked 23 hours ago"],
    [DAY, "Checked yesterday"],
    [3 * DAY + 5 * HOUR, "Checked 3 days ago"],
  ])("shows a check that is %i seconds old as %s", (seconds, text) =>
    atNow(async () => {
      const view = await open({
        "GET /api/session": admin,
        "GET /api/extensions": extensions,
        "GET /api/extensions/updates": () => checkedAt(secondsAgo(seconds)),
      });
      expect(view.getByText(text)).toBeTruthy();
    }),
  );

  it("tells the age in the language of the shell", () =>
    atNow(async () => {
      const view = await open({
        "GET /api/session": admin,
        "GET /api/extensions": extensions,
        "GET /api/extensions/updates": () => checkedAt(minutesAgo(3)),
      });
      view.shell.setLocale("de");
      await settle();
      expect(view.getByText("Geprüft vor 3 Minuten")).toBeTruthy();
    }));

  it.each([
    [90 * 60, "Geprüft vor 1 Stunde"],
    [5 * HOUR, "Geprüft vor 5 Stunden"],
    [DAY, "Geprüft gestern"],
    [3 * DAY, "Geprüft vor 3 Tagen"],
  ])("tells an age of %i seconds in hours and days in German: %s", (seconds, text) =>
    atNow(async () => {
      const view = await open({
        "GET /api/session": admin,
        "GET /api/extensions": extensions,
        "GET /api/extensions/updates": () => checkedAt(secondsAgo(seconds)),
      });
      view.shell.setLocale("de");
      await settle();
      expect(view.getByText(text)).toBeTruthy();
    }),
  );

  it("shows no age before the first check and forgets it when admin mode ends", () =>
    atNow(async () => {
      const unchecked = await open({
        "GET /api/session": () => json(200, sessionInfo({ admin: false, canAdmin: true })),
        "GET /api/extensions": extensions,
      });
      expect(unchecked.queryByText(/^Checked/)).toBeNull();
      cleanup();

      const view = await open({
        "GET /api/session": admin,
        "GET /api/extensions": extensions,
        "GET /api/extensions/updates": () => checkedAt(NOW),
        "DELETE /api/session/admin": leaveAdmin,
      });
      expect(view.getByText("Checked just now")).toBeTruthy();
      await view.shell.session.disableAdmin();
      await settle();
      expect(view.queryByText(/^Checked/)).toBeNull();
    }));

  it("keeps the age up to date while the page is open and stops doing so when it is closed", async () => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval", "Date"] });
    try {
      vi.setSystemTime(NOW);
      const view = await open({
        "GET /api/session": admin,
        "GET /api/extensions": extensions,
        "GET /api/extensions/updates": () => checkedAt(NOW),
      });
      expect(view.getByText("Checked just now")).toBeTruthy();
      vi.advanceTimersByTime(3 * 60_000);
      expect(view.getByText("Checked 3 minutes ago")).toBeTruthy();
      expect(vi.getTimerCount()).toBe(1);
      cleanup();
      expect(vi.getTimerCount()).toBe(0);
    } finally {
      vi.useRealTimers();
    }
  });

  it("shows the last result with a note when GitHub does not answer", () =>
    atNow(async () => {
      let fresh = false;
      const answer = () => (fresh ? checkedAt(NOW) : staleAnswer());
      const view = await open({
        "GET /api/session": admin,
        "GET /api/extensions": extensions,
        "GET /api/extensions/updates": answer,
        "GET /api/extensions/updates?refresh=1": answer,
      });
      expect(hint(view)?.textContent).toBe(
        `GitHub does not answer right now (GitHub's request limit is reached. Try again later or configure a token); shown is the check of ${stamp(minutesAgo(7))}.`,
      );
      expect(view.getByText("Checked 7 minutes ago")).toBeTruthy();
      expect(view.getByRole("button", { name: "Update to 0.2.0" })).toBeTruthy(); // the last result is still offered

      fresh = true; // GitHub answers again
      fireEvent.click(view.getByRole("button", { name: "Check for updates" }));
      await waitFor(() => expect(hint(view)).toBeNull());
      expect(view.queryByRole("button", { name: "Update to 0.2.0" })).toBeNull();
      expect(view.getByText("Checked just now")).toBeTruthy();
    }));

  it.each([
    ["github_unavailable", "dial tcp: refused", "GitHub could not be reached: dial tcp: refused"],
    ["timeout", "context deadline exceeded", "GitHub did not answer in time. Try again in a moment"],
    ["something_new", "the message of the core", "the message of the core"], // a code the shell does not know
  ])("tells why GitHub does not answer in its own words: %s", (code, message, reason) =>
    atNow(async () => {
      const view = await open({
        "GET /api/session": admin,
        "GET /api/extensions": extensions,
        "GET /api/extensions/updates": () => staleAnswer(code, message),
      });
      expect(hint(view)?.textContent).toBe(
        `GitHub does not answer right now (${reason}); shown is the check of ${stamp(minutesAgo(7))}.`,
      );
    }),
  );

  it("tells why GitHub does not answer in German", () =>
    atNow(async () => {
      const view = await open({
        "GET /api/session": admin,
        "GET /api/extensions": extensions,
        "GET /api/extensions/updates": () => staleAnswer(),
      });
      view.shell.setLocale("de");
      await settle();
      expect(hint(view)?.textContent).toBe(
        `GitHub antwortet gerade nicht (Das Anfragelimit von GitHub ist erreicht. Versuche es später oder hinterlege einen Token); angezeigt ist die Prüfung von ${stamp(minutesAgo(7), "de")}.`,
      );
    }));

  // the kept result can be from another day: the note names the date with the time, in the language of the shell
  it.each([
    ["en", "shown is the check of ", /check of \d{1,2}\/\d{1,2}\/\d{2}, \d{1,2}:\d{2}/],
    ["de", "angezeigt ist die Prüfung von ", /Prüfung von \d{1,2}\.\d{1,2}\.\d{2}, \d{2}:\d{2}\.$/],
  ])("names the date and the time of the kept check in the stale note (%s)", (locale, lead, shape) =>
    atNow(async () => {
      const kept = new Date(NOW.getTime() - 2 * DAY - 25 * 60_000); // two days and 25 minutes ago
      const view = await open({
        "GET /api/session": admin,
        "GET /api/extensions": extensions,
        "GET /api/extensions/updates": () => checkedAt(kept, { updates: [UPDATE], stale: true }),
      });
      view.shell.setLocale(locale as "en" | "de");
      await settle();
      const text = hint(view)?.textContent ?? "";
      expect(text).toMatch(shape);
      expect(text.endsWith(`${lead}${stamp(kept, locale)}.`)).toBe(true);
    }),
  );

  // the core says how old a check is: a browser clock that is wrong makes no difference
  it.each([
    ["ahead", 3 * 3600_000],
    ["behind", -3 * 3600_000],
  ])("shows the age of the check by the core even when the browser clock is %s", (_name, offset) =>
    atNow(async () => {
      const view = await open({
        "GET /api/session": admin,
        "GET /api/extensions": extensions,
        "GET /api/extensions/updates": () => checkedAt(minutesAgo(2)),
      });
      expect(view.getByText("Checked 2 minutes ago")).toBeTruthy();
    }, NOW.getTime() + offset),
  );

  it("asks GitHub for the check that follows the admin prompt of the button", async () => {
    const view = await open({
      "GET /api/session": () => json(200, sessionInfo({ admin: false, canAdmin: true })),
      "GET /api/extensions": extensions,
      "GET /api/extensions/updates": noUpdates,
      "GET /api/extensions/updates?refresh=1": noUpdates,
      "POST /api/session/admin": admin,
    });
    fireEvent.click(view.getByRole("button", { name: "Check for updates" }));
    await waitFor(() => expect(view.shell.session.adminRequested()).toBe(true));
    await view.shell.session.enableAdmin("pw");
    await settle();
    expect(checkCalls(view)).toEqual(["/api/extensions/updates?refresh=1"]);
  });

  it("does not ask GitHub for the check of an admin mode that comes on later, after the prompt was declined", async () => {
    const view = await open({
      "GET /api/session": () => json(200, sessionInfo({ admin: false, canAdmin: true })),
      "GET /api/extensions": extensions,
      "GET /api/extensions/updates": noUpdates,
      "GET /api/extensions/updates?refresh=1": noUpdates,
      "POST /api/session/admin": admin,
    });
    fireEvent.click(view.getByRole("button", { name: "Check for updates" }));
    await waitFor(() => expect(view.shell.session.adminRequested()).toBe(true));
    view.shell.session.answerAdminRequest(false);
    await settle();
    await view.shell.session.enableAdmin("pw");
    await settle();
    expect(checkCalls(view)).toEqual(["/api/extensions/updates"]);
  });

  it("toasts the errors of a check once, not again when the core answers from its cache", async () => {
    let at = minutesAgo(1);
    const answer = () => checkedAt(at, { errors: [errorOfDocker] });
    const view = await open({
      "GET /api/session": admin,
      "GET /api/extensions": extensions,
      "GET /api/extensions/updates": answer,
      "GET /api/extensions/updates?refresh=1": answer,
    });
    expect(toasts(view)).toEqual([toastOfDocker]);

    // the same check comes back (checkedAt unchanged): its errors were shown already
    fireEvent.click(view.getByRole("button", { name: "Check for updates" }));
    await settle();
    expect(checkCalls(view)).toHaveLength(2);
    expect(toasts(view)).toEqual([toastOfDocker]);

    // a new check with the same error is news again
    at = NOW;
    fireEvent.click(view.getByRole("button", { name: "Check for updates" }));
    await settle();
    expect(checkCalls(view)).toHaveLength(3);
    expect(toasts(view)).toEqual([toastOfDocker, toastOfDocker]);
  });

  it("does not toast the errors of a cached check again when the page is visited again", async () => {
    const answer = () => checkedAt(minutesAgo(1), { errors: [errorOfDocker] });
    const view = await open({
      "GET /api/session": admin,
      "GET /api/extensions": extensions,
      "GET /api/extensions/updates": answer,
    });
    expect(toasts(view)).toEqual([toastOfDocker]);

    const again = remount(view, () => <ExtensionsPage />);
    await settle();
    expect(checkCalls(again)).toHaveLength(2); // the page that came back asked, and got the same check from the cache
    expect(toasts(again)).toEqual([toastOfDocker]);
  });
});

describe("extensions page: remove", () => {
  const confirmRemove = async (view: View) => {
    fireEvent.click(view.getByRole("button", { name: "Remove Hello" }));
    const dialog = await view.findByRole("dialog", { name: "Remove extension" });
    expect(dialog.textContent).toContain("Its running processes will be stopped and its data deleted.");
    fireEvent.click(within(dialog).getByRole("button", { name: "Remove" }));
    await settle();
  };

  it("removes the extension and says so", async () => {
    let removed = false;
    const view = await open({
      "GET /api/session": admin,
      "GET /api/extensions": () => json(200, { extensions: removed ? [] : [hello("0.1.0")] }),
      "GET /api/extensions/updates": noUpdates,
      "DELETE /api/extensions/hello": () => {
        removed = true;
        return new Response(null, { status: 204 });
      },
    });
    await confirmRemove(view);
    await waitFor(() => expect(toasts(view)).toEqual(["Hello was removed."]));
    expect(view.queryByRole("button", { name: "Remove Hello" })).toBeNull();
  });

  it("shows the answer when only the data directory could not be deleted, and drops the extension from the list", async () => {
    const message =
      "the extension was removed, but its data directory /var/lib/edelweiss-ext-hello could not be deleted";
    let removed = false;
    const view = await open({
      "GET /api/session": admin,
      "GET /api/extensions": () => json(200, { extensions: removed ? [] : [hello("0.1.0")] }),
      "GET /api/extensions/updates": noUpdates,
      "DELETE /api/extensions/hello": () => {
        removed = true;
        return json(500, { error: { code: "data_not_removed", message } });
      },
    });
    await confirmRemove(view);
    // the message says what happened; "Could not remove Hello: the extension was removed" would contradict itself
    await waitFor(() => expect(toasts(view)).toEqual([message]));
    expect(view.queryByRole("button", { name: "Remove Hello" })).toBeNull();
  });

  it("names an extension that could not be removed at all", async () => {
    const view = await open({
      "GET /api/session": admin,
      "GET /api/extensions": () => json(200, { extensions: [hello("0.1.0")] }),
      "GET /api/extensions/updates": noUpdates,
      "DELETE /api/extensions/hello": () =>
        json(500, { error: { code: "internal_error", message: "could not remove extension" } }),
    });
    await confirmRemove(view);
    await waitFor(() => expect(toasts(view)).toEqual(["Could not remove Hello: could not remove extension"]));
    expect(view.getByRole("button", { name: "Remove Hello" })).toBeTruthy();
  });
});
