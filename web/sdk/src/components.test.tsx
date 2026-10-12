import { cleanup, fireEvent, render } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "./api";
import { AdminRequired, ConfirmDialog, ErrorAlert, LoadingState, PageHeader } from "./components";
import { fakeSession } from "./test-utils";
import { TestShell } from "./testing";

afterEach(cleanup);

describe("components", () => {
  it("PageHeader shows title, subtitle and actions", () => {
    const { getByRole, getByText } = render(() => (
      <PageHeader title="Overview" subtitle="alpine" actions={<button type="button">act</button>} />
    ));
    expect(getByRole("heading").textContent).toBe("Overview");
    expect(getByText("alpine")).toBeTruthy();
    expect(getByRole("button").textContent).toBe("act");
  });

  it("LoadingState uses the translated default label", () => {
    const { getByRole } = render(() => (
      <TestShell locale="de">
        <LoadingState />
      </TestShell>
    ));
    expect(getByRole("status").textContent).toContain("Wird geladen");
  });

  it("ErrorAlert shows the error message and retries", () => {
    const retry = vi.fn();
    const { getByRole, getByText } = render(() => (
      <TestShell>
        <ErrorAlert error={new ApiError(500, "internal_error", "boom")} onRetry={retry} />
      </TestShell>
    ));
    expect(getByText("boom")).toBeTruthy();
    fireEvent.click(getByRole("button", { name: "Retry" }));
    expect(retry).toHaveBeenCalledOnce();
  });

  it("ConfirmDialog renders only when open and reports the choice", () => {
    const onConfirm = vi.fn();
    const onCancel = vi.fn();
    const { getByRole, queryByRole } = render(() => (
      <TestShell>
        <ConfirmDialog
          open
          title="Remove?"
          message="Really"
          danger
          onConfirm={onConfirm}
          onCancel={onCancel}
        />
        <ConfirmDialog open={false} title="Hidden" message="x" onConfirm={onConfirm} onCancel={onCancel} />
      </TestShell>
    ));
    expect(queryByRole("dialog", { name: "Hidden" })).toBeNull();
    const dialog = getByRole("dialog", { name: "Remove?" });
    expect(dialog.textContent).toContain("Really");
    fireEvent.click(getByRole("button", { name: "Cancel" }));
    fireEvent.click(getByRole("button", { name: "Confirm" }));
    expect(onCancel).toHaveBeenCalledOnce();
    expect(onConfirm).toHaveBeenCalledOnce();
  });

  it("ConfirmDialog focuses Cancel and cancels on Escape and on the backdrop", () => {
    const onCancel = vi.fn();
    const { getByRole, container } = render(() => (
      <TestShell>
        <ConfirmDialog open title="Remove?" message="Really" onConfirm={() => {}} onCancel={onCancel} />
      </TestShell>
    ));
    expect(document.activeElement).toBe(getByRole("button", { name: "Cancel" }));
    fireEvent.keyDown(document, { key: "Escape" });
    fireEvent.click(container.querySelector(".modal-backdrop")!);
    expect(onCancel).toHaveBeenCalledTimes(2);
  });

  it("ConfirmDialog focuses Cancel at every opening and stops listening when it closes", () => {
    const onCancel = vi.fn();
    const [open, setOpen] = createSignal(true);
    const { getByRole, queryByRole } = render(() => (
      <TestShell>
        <ConfirmDialog
          open={open()}
          title="Remove?"
          message="Really"
          onConfirm={() => {}}
          onCancel={onCancel}
        />
      </TestShell>
    ));
    expect(document.activeElement).toBe(getByRole("button", { name: "Cancel" }));
    getByRole("button", { name: "Confirm" }).focus(); // the user moved on
    setOpen(false);
    expect(queryByRole("dialog")).toBeNull();
    fireEvent.keyDown(document, { key: "Escape" });
    expect(onCancel).not.toHaveBeenCalled();

    setOpen(true);
    expect(document.activeElement).toBe(getByRole("button", { name: "Cancel" }));
    fireEvent.keyDown(document, { key: "Escape" });
    expect(onCancel).toHaveBeenCalledOnce();
  });

  it("ConfirmDialog ignores Escape while it is closed", () => {
    const onCancel = vi.fn();
    render(() => (
      <TestShell>
        <ConfirmDialog open={false} title="Hidden" message="x" onConfirm={() => {}} onCancel={onCancel} />
      </TestShell>
    ));
    fireEvent.keyDown(document, { key: "Escape" });
    expect(onCancel).not.toHaveBeenCalled();
  });

  it("ConfirmDialog while busy disables both buttons, shows the spinner and ignores every way to leave or repeat", () => {
    const onConfirm = vi.fn();
    const onCancel = vi.fn();
    const [busy, setBusy] = createSignal(false);
    const { getByRole, container } = render(() => (
      <TestShell>
        <ConfirmDialog
          open
          title="Restart?"
          message="Really"
          busy={busy()}
          onConfirm={onConfirm}
          onCancel={onCancel}
        />
      </TestShell>
    ));
    const confirm = getByRole("button", { name: "Confirm" }) as HTMLButtonElement;
    const cancel = getByRole("button", { name: "Cancel" }) as HTMLButtonElement;
    expect(confirm.disabled || cancel.disabled).toBe(false);
    expect(container.querySelector(".loading")).toBeNull();

    setBusy(true);
    expect(confirm.disabled && cancel.disabled).toBe(true);
    expect(confirm.getAttribute("aria-busy")).toBe("true");
    expect(confirm.querySelector(".loading")).not.toBeNull();
    fireEvent.click(confirm);
    fireEvent.click(cancel);
    fireEvent.keyDown(document, { key: "Escape" });
    fireEvent.click(container.querySelector(".modal-backdrop")!);
    expect(onConfirm).not.toHaveBeenCalled();
    expect(onCancel).not.toHaveBeenCalled();

    setBusy(false);
    fireEvent.click(confirm);
    expect(onConfirm).toHaveBeenCalledOnce();
  });

  it("AdminRequired offers admin mode only to admin-capable users", () => {
    const capable = fakeSession({ canAdmin: true });
    const { getByRole, unmount } = render(() => (
      <TestShell session={capable.api}>
        <AdminRequired />
      </TestShell>
    ));
    fireEvent.click(getByRole("button", { name: "Enable admin mode" }));
    expect(capable.calls.requestAdmin).toBe(1);
    unmount();
    const plain = fakeSession({ canAdmin: false });
    const { queryByRole } = render(() => (
      <TestShell session={plain.api}>
        <AdminRequired />
      </TestShell>
    ));
    expect(queryByRole("button")).toBeNull();
  });
});
