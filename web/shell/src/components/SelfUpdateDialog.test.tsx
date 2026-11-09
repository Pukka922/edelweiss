import { cleanup, fireEvent, screen } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import { mount } from "../test/mount";
import { SelfUpdateDialog, type SelfUpdateTarget } from "./SelfUpdateDialog";

afterEach(cleanup);

const SUM = "ab".repeat(32);
const target = (over: Partial<SelfUpdateTarget> = {}): SelfUpdateTarget => ({
  repo: "Pukka922/edelweiss",
  offer: {
    current: "0.3.1",
    latest: "0.4.0",
    available: true,
    release: "v0.4.0",
    notesUrl: "https://github.com/Pukka922/edelweiss/releases/tag/v0.4.0",
    asset: "edelweissd_0.4.0_linux_amd64",
    sha256: SUM,
  },
  ...over,
});
const noRoutes = {};

describe("SelfUpdateDialog", () => {
  it("shows repository, release, file and checksum and the warning", () => {
    const view = mount(noRoutes, () => (
      <SelfUpdateDialog target={target()} busy={false} onConfirm={() => {}} onCancel={() => {}} />
    ));
    const dialog = view.getByRole("dialog", { name: "Update edelweiss" });
    expect(dialog.textContent).toContain("Pukka922/edelweiss");
    expect(dialog.textContent).toContain("edelweissd_0.4.0_linux_amd64");
    expect(dialog.textContent).toContain(SUM);
    expect(dialog.textContent).toContain("0.3.1");
    expect(dialog.textContent).toContain("0.4.0");
    const release = view.getByRole("link", { name: "v0.4.0" }) as HTMLAnchorElement;
    expect(release.href).toBe("https://github.com/Pukka922/edelweiss/releases/tag/v0.4.0");
    expect(release.rel).toContain("noopener");
    expect(release.target).toBe("_blank");
    expect(view.getByText(/edelweiss restarts\. Everybody is signed out/)).toBeTruthy();
    expect(view.queryByText(/not backed up in lbu/)).toBeNull();
  });

  it("says the German warning word for word", () => {
    const view = mount(noRoutes, () => (
      <SelfUpdateDialog target={target()} busy={false} onConfirm={() => {}} onCancel={() => {}} />
    ));
    view.shell.setLocale("de");
    expect(
      view.getByText(
        "edelweiss startet neu. Alle werden abgemeldet, offene Konsolen und Uploads brechen ab, die Extensions starten neu.",
      ),
    ).toBeTruthy();
  });

  it("warns that the update is lost after a host restart when the binary is not in lbu", () => {
    const view = mount(noRoutes, () => (
      <SelfUpdateDialog
        target={target({ lbuWarning: true })}
        busy={false}
        onConfirm={() => {}}
        onCancel={() => {}}
      />
    ));
    expect(view.getByText(/not backed up in lbu/)).toBeTruthy();
    view.shell.setLocale("de");
    expect(
      view.getByText(
        "Das Update geht nach einem Neustart des Hosts verloren, weil das Binary nicht in lbu gesichert ist.",
      ),
    ).toBeTruthy();
  });

  it("confirms and cancels, focuses cancel, and closes on Escape", () => {
    const onConfirm = vi.fn();
    const onCancel = vi.fn();
    const view = mount(noRoutes, () => (
      <SelfUpdateDialog target={target()} busy={false} onConfirm={onConfirm} onCancel={onCancel} />
    ));
    expect(document.activeElement).toBe(view.getByRole("button", { name: "Cancel" }));
    fireEvent.click(view.getByRole("button", { name: "Update" }));
    expect(onConfirm).toHaveBeenCalledTimes(1);
    fireEvent.keyDown(document, { key: "Escape" });
    expect(onCancel).toHaveBeenCalledTimes(1);
  });

  it("keeps Tab inside the dialog: the last control wraps to the first and back", () => {
    const view = mount(noRoutes, () => (
      <SelfUpdateDialog target={target()} busy={false} onConfirm={() => {}} onCancel={() => {}} />
    ));
    const first = view.getByRole("link", { name: "v0.4.0" });
    const last = view.getByRole("button", { name: "Update" });
    last.focus();
    const forward = new KeyboardEvent("keydown", { key: "Tab", bubbles: true, cancelable: true });
    document.dispatchEvent(forward);
    expect(document.activeElement).toBe(first);
    expect(forward.defaultPrevented).toBe(true);
    const back = new KeyboardEvent("keydown", {
      key: "Tab",
      shiftKey: true,
      bubbles: true,
      cancelable: true,
    });
    document.dispatchEvent(back);
    expect(document.activeElement).toBe(last);
    // in the middle the browser moves the focus itself
    view.getByRole("button", { name: "Cancel" }).focus();
    const middle = new KeyboardEvent("keydown", { key: "Tab", bubbles: true, cancelable: true });
    document.dispatchEvent(middle);
    expect(middle.defaultPrevented).toBe(false);
  });

  it("cannot be cancelled while it works and shows the progress and an error", () => {
    const onCancel = vi.fn();
    const view = mount(noRoutes, () => (
      <SelfUpdateDialog
        target={target()}
        busy
        progress="Downloading and checking …"
        error="That did not work"
        onConfirm={() => {}}
        onCancel={onCancel}
      />
    ));
    expect(view.getByText("Downloading and checking …")).toBeTruthy();
    expect(view.getByRole("alert").textContent).toBe("That did not work");
    fireEvent.keyDown(document, { key: "Escape" });
    expect(onCancel).not.toHaveBeenCalled();
    expect((screen.getByRole("button", { name: "Cancel" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("is not there without a target", () => {
    const view = mount(noRoutes, () => (
      <SelfUpdateDialog target={undefined} busy={false} onConfirm={() => {}} onCancel={() => {}} />
    ));
    expect(view.queryByRole("dialog")).toBeNull();
  });
});
