import { type JSX, onCleanup, onMount, Show } from "solid-js";
import { useI18n } from "./i18n";
import { useSession } from "./session";

export function PageHeader(props: { title: string; subtitle?: string; actions?: JSX.Element }): JSX.Element {
  return (
    <header class="mb-6 flex flex-wrap items-center justify-between gap-4">
      <div>
        <h1 class="text-2xl font-semibold">{props.title}</h1>
        <Show when={props.subtitle}>
          <p class="text-base-content/70">{props.subtitle}</p>
        </Show>
      </div>
      <Show when={props.actions}>
        <div class="flex flex-wrap gap-2">{props.actions}</div>
      </Show>
    </header>
  );
}

export function LoadingState(props: { label?: string }): JSX.Element {
  const { t } = useI18n("sdk");
  return (
    <div class="flex items-center gap-3 p-6 text-base-content/70" role="status">
      <span class="loading loading-spinner" />
      {props.label ?? t("loading")}
    </div>
  );
}

export function ErrorAlert(props: { error: unknown; onRetry?: () => void }): JSX.Element {
  const { t } = useI18n("sdk");
  const message = () => (props.error instanceof Error ? props.error.message : String(props.error));
  return (
    <div role="alert" class="alert alert-error">
      <div>
        <p class="font-semibold">{t("error.title")}</p>
        <p class="text-sm">{message()}</p>
      </div>
      <Show when={props.onRetry}>
        <button type="button" class="btn btn-sm" onClick={() => props.onRetry?.()}>
          {t("error.retry")}
        </button>
      </Show>
    </div>
  );
}

export interface ConfirmDialogProps {
  open: boolean;
  title: string;
  message: string;
  confirmLabel?: string;
  danger?: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}

/** The open dialog. Every opening focuses Cancel, the safe choice; Escape and the backdrop cancel. */
function ConfirmPrompt(props: Omit<ConfirmDialogProps, "open">): JSX.Element {
  const { t } = useI18n("sdk");
  let cancelButton: HTMLButtonElement | undefined;
  const onKeyDown = (e: KeyboardEvent) => {
    if (e.key === "Escape") props.onCancel();
  };
  onMount(() => {
    cancelButton?.focus();
    document.addEventListener("keydown", onKeyDown);
  });
  onCleanup(() => document.removeEventListener("keydown", onKeyDown));
  return (
    <div class="modal modal-open" role="dialog" aria-modal="true" aria-label={props.title}>
      <div class="modal-box">
        <h3 class="text-lg font-bold">{props.title}</h3>
        <p class="py-4">{props.message}</p>
        <div class="modal-action">
          <button ref={cancelButton} type="button" class="btn" onClick={() => props.onCancel()}>
            {t("confirm.cancel")}
          </button>
          <button
            type="button"
            class={props.danger ? "btn btn-error" : "btn btn-primary"}
            onClick={() => props.onConfirm()}
          >
            {props.confirmLabel ?? t("confirm.confirm")}
          </button>
        </div>
      </div>
      <div class="modal-backdrop" aria-hidden="true" onClick={() => props.onCancel()} />
    </div>
  );
}

export function ConfirmDialog(props: ConfirmDialogProps): JSX.Element {
  return (
    <Show when={props.open}>
      <ConfirmPrompt
        title={props.title}
        message={props.message}
        confirmLabel={props.confirmLabel}
        danger={props.danger}
        onConfirm={props.onConfirm}
        onCancel={props.onCancel}
      />
    </Show>
  );
}

export function AdminRequired(props: { message?: string }): JSX.Element {
  const session = useSession();
  const { t } = useI18n("sdk");
  return (
    <div class="alert alert-warning" role="alert">
      <span>{props.message ?? t("admin.required")}</span>
      <Show when={session.canAdmin()}>
        <button type="button" class="btn btn-sm btn-warning" onClick={() => void session.requestAdmin()}>
          {t("admin.enable")}
        </button>
      </Show>
    </div>
  );
}
