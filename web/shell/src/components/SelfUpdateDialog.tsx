import { useI18n } from "@edelweiss/sdk";
import { onCleanup, onMount, Show } from "solid-js";
import type { Offer } from "../about";

export interface SelfUpdateTarget {
  repo: string;
  offer: Offer;
  /** The new binary would not be saved by lbu. */
  lbuWarning?: boolean;
}

/** The confirmation itself: it only exists while the dialog is open, so every opening starts clean. */
function SelfUpdatePrompt(props: {
  target: SelfUpdateTarget;
  busy: boolean;
  progress: string | undefined;
  error: string | undefined;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  const { t } = useI18n("shell");
  let cancelButton: HTMLButtonElement | undefined;
  let box: HTMLDivElement | undefined;

  // An update that has been started goes on even if the dialog closes: it cannot be cancelled then.
  const cancel = () => {
    if (!props.busy) props.onCancel();
  };
  const onKeyDown = (e: KeyboardEvent) => {
    if (e.key === "Escape") cancel();
    if (e.key === "Tab") trap(e);
  };
  // Tab stays inside the dialog: from the last control it wraps to the first, from the first back to the last.
  const trap = (e: KeyboardEvent) => {
    const controls = [...(box?.querySelectorAll<HTMLElement>("a[href], button:not([disabled])") ?? [])];
    const first = controls[0];
    const last = controls[controls.length - 1];
    if (!first || !last) {
      e.preventDefault();
      return;
    }
    const active = document.activeElement;
    if (!box?.contains(active)) {
      e.preventDefault();
      first.focus();
    } else if (e.shiftKey && active === first) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && active === last) {
      e.preventDefault();
      first.focus();
    }
  };
  onMount(() => {
    cancelButton?.focus(); // the safe answer first
    document.addEventListener("keydown", onKeyDown);
  });
  onCleanup(() => document.removeEventListener("keydown", onKeyDown));

  const offer = () => props.target.offer;
  const release = () => offer().release ?? offer().latest ?? "";
  return (
    <div class="modal modal-open" role="dialog" aria-modal="true" aria-label={t("about.dialog.title")}>
      <div ref={box} class="modal-box flex max-w-2xl flex-col gap-4">
        <h3 class="text-lg font-bold">{t("about.dialog.title")}</h3>
        <Show when={props.error}>
          {(message) => (
            <div role="alert" class="alert alert-error text-sm">
              {message()}
            </div>
          )}
        </Show>
        <p>{t("about.dialog.message", { from: offer().current, to: offer().latest ?? release() })}</p>
        <dl class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-sm">
          <dt class="text-base-content/60">{t("about.dialog.repository")}</dt>
          <dd class="break-all font-mono">{props.target.repo}</dd>
          <dt class="text-base-content/60">{t("about.dialog.release")}</dt>
          <dd>
            <Show when={offer().notesUrl} fallback={release()}>
              {(url) => (
                <a class="link" href={url()} target="_blank" rel="noopener noreferrer">
                  {release()}
                </a>
              )}
            </Show>
          </dd>
          <dt class="text-base-content/60">{t("about.dialog.asset")}</dt>
          <dd class="break-all">{offer().asset}</dd>
          <dt class="text-base-content/60">{t("about.dialog.sha256")}</dt>
          <dd class="break-all font-mono text-xs">{offer().sha256}</dd>
        </dl>
        <div class="alert alert-warning text-sm">{t("about.dialog.warning")}</div>
        <Show when={props.target.lbuWarning}>
          <div class="alert alert-warning text-sm">{t("about.dialog.lbuWarning")}</div>
        </Show>
        <Show when={props.progress}>
          {(text) => (
            <p role="status" class="flex items-center gap-2 text-sm">
              <span class="loading loading-spinner loading-sm" aria-hidden="true" />
              {text()}
            </p>
          )}
        </Show>
        <div class="modal-action">
          <button ref={cancelButton} type="button" class="btn" disabled={props.busy} onClick={cancel}>
            {t("about.cancel")}
          </button>
          <button
            type="button"
            class="btn btn-primary"
            disabled={props.busy}
            aria-busy={props.busy}
            onClick={() => props.onConfirm()}
          >
            {t("about.update")}
          </button>
        </div>
      </div>
      <div class="modal-backdrop" aria-hidden="true" onClick={cancel} />
    </div>
  );
}

/** Confirmation of the update of edelweiss itself: release, file and checksum, and the warning that everybody is signed out. */
export function SelfUpdateDialog(props: {
  target: SelfUpdateTarget | undefined;
  /** True from the confirmation on; the dialog cannot be cancelled then. */
  busy: boolean;
  /** What is going on while busy ("Downloading and checking …"). */
  progress?: string;
  /** Why the last attempt was refused; the dialog stays open for the user to read it, cancel or try again. */
  error?: string;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  return (
    <Show when={props.target}>
      {(target) => (
        <SelfUpdatePrompt
          target={target()}
          busy={props.busy}
          progress={props.progress}
          error={props.error}
          onConfirm={props.onConfirm}
          onCancel={props.onCancel}
        />
      )}
    </Show>
  );
}
