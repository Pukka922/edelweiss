import type { ExtensionInfo } from "@edelweiss/sdk";
import { sessionInfo } from "@edelweiss/sdk/testing";
import { cleanup, fireEvent, waitFor, within } from "@solidjs/testing-library";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { json } from "../test/fake-fetch";
import { ext, noUpdates } from "../test/fixtures";
import { mount, settle } from "../test/mount";
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
const offerUpdate = () => json(200, { updates: [UPDATE], errors: [] });

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
      ...ext("docker", "containers", 10, "Docker"),
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
    await waitFor(() => expect(button("Remove").disabled).toBe(true));
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
    await waitFor(() => expect(button("Remove").disabled).toBe(false));
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
    let checks = 0;
    const view = await open({
      "GET /api/session": admin,
      "GET /api/extensions": () => json(200, { extensions: [hello("0.1.0")] }),
      "GET /api/extensions/updates": () => {
        checks++;
        const newer = { ...UPDATE, available: "0.3.0", tag: "v0.3.0", sha256: NEWER_SUM };
        return json(200, { updates: [checks === 1 ? UPDATE : newer], errors: [] });
      },
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

    // the entry with the old checksum is gone: the release as it is now is offered
    expect(await view.findByRole("button", { name: "Update to 0.3.0" })).toBeTruthy();
    expect(view.queryByRole("button", { name: "Update to 0.2.0" })).toBeNull();
    expect(checks).toBe(2);
  });

  it("keeps the dialog open and names the missing packages when the core refuses an update for them", async () => {
    const view = await open({
      "GET /api/session": admin,
      "GET /api/extensions": () => json(200, { extensions: [hello("0.1.0")] }),
      "GET /api/extensions/updates": offerUpdate,
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

describe("extensions page: remove", () => {
  const confirmRemove = async (view: View) => {
    fireEvent.click(view.getByRole("button", { name: "Remove" }));
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
    expect(view.queryByRole("button", { name: "Remove" })).toBeNull();
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
    expect(view.queryByRole("button", { name: "Remove" })).toBeNull();
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
    expect(view.getByRole("button", { name: "Remove" })).toBeTruthy();
  });
});
