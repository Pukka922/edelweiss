import { type ExtensionInfo, localized, useI18n } from "@edelweiss/sdk";
import { onCleanup, onMount, Show } from "solid-js";
import type { Update } from "../state/installer";

export interface UpdateTarget {
  ext: ExtensionInfo;
  update: Update;
}

/** The confirmation itself: it only exists while the dialog is open, so every opening starts clean. */
function UpdatePrompt(props: {
  target: UpdateTarget;
  busy: boolean;
  error: string | undefined;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  const { t, locale } = useI18n("shell");
  let cancelButton: HTMLButtonElement | undefined;

  // An update that is running finishes first: the server keeps going even if the dialog closes.
  const cancel = () => {
    if (!props.busy) props.onCancel();
  };
  const onKeyDown = (e: KeyboardEvent) => {
    if (e.key === "Escape") cancel();
  };
  onMount(() => {
    cancelButton?.focus(); // the safe answer first
    document.addEventListener("keydown", onKeyDown);
  });
  onCleanup(() => document.removeEventListener("keydown", onKeyDown));

  const update = () => props.target.update;
  return (
    <div class="modal modal-open" role="dialog" aria-modal="true" aria-label={t("extensions.updateTitle")}>
      <div class="modal-box flex max-w-2xl flex-col gap-4">
        <h3 class="text-lg font-bold">{t("extensions.updateTitle")}</h3>
        <Show when={props.error}>
          {(message) => (
            <div role="alert" class="alert alert-error text-sm">
              {message()}
            </div>
          )}
        </Show>
        <p>
          {t("extensions.updateMessage", {
            name: localized(props.target.ext.name, locale()),
            from: update().installed,
            to: update().available,
          })}
        </p>
        <dl class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-sm">
          <dt class="text-base-content/60">{t("installer.repository")}</dt>
          <dd class="font-mono">{update().repo}</dd>
          <dt class="text-base-content/60">{t("installer.release")}</dt>
          <dd>{update().tag}</dd>
          <dt class="text-base-content/60">{t("extensions.version")}</dt>
          <dd>
            {update().installed} → {update().available}
          </dd>
          <dt class="text-base-content/60">{t("installer.asset")}</dt>
          <dd class="break-all">{update().asset}</dd>
          <dt class="text-base-content/60">{t("installer.sha256")}</dt>
          <dd class="break-all font-mono text-xs">{update().sha256}</dd>
        </dl>
        <div class="alert alert-warning text-sm">{t("installer.warning")}</div>
        <div class="modal-action">
          <button ref={cancelButton} type="button" class="btn" disabled={props.busy} onClick={cancel}>
            {t("installer.cancel")}
          </button>
          <button
            type="button"
            class="btn btn-primary"
            disabled={props.busy}
            onClick={() => props.onConfirm()}
          >
            <Show when={props.busy}>
              <span class="loading loading-spinner loading-sm" />
            </Show>
            {t("extensions.update")}
          </button>
        </div>
      </div>
      <div class="modal-backdrop" aria-hidden="true" onClick={cancel} />
    </div>
  );
}

/** Confirmation of an update: shows the release, the file and its checksum as an install does, and the same warning. */
export function UpdateDialog(props: {
  target: UpdateTarget | undefined;
  /** True while the update runs; it cannot be cancelled then. */
  busy: boolean;
  /** Why the last attempt failed, shown in the dialog; it stays open for the user to read it, cancel or try again. */
  error?: string;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  return (
    <Show when={props.target}>
      {(target) => (
        <UpdatePrompt
          target={target()}
          busy={props.busy}
          error={props.error}
          onConfirm={props.onConfirm}
          onCancel={props.onCancel}
        />
      )}
    </Show>
  );
}
