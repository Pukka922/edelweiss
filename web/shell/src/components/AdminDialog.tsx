import { ApiError, type Translator, useI18n } from "@edelweiss/sdk";
import { createSignal, onCleanup, onMount, Show } from "solid-js";
import { useShell } from "../context";

function adminError(err: unknown, t: Translator["t"]): string {
  if (err instanceof ApiError) {
    if (err.code === "invalid_credentials") return t("admin.invalid");
    if (err.code === "admin_not_permitted") return t("admin.notPermitted");
    if (err.code === "too_many_requests") return t("admin.tooMany");
    return t("admin.failed", { message: err.message });
  }
  return t("admin.failed", { message: String(err) });
}

/** The password prompt itself. It only exists while a request is pending, so every opening starts clean. */
function AdminPrompt() {
  const shell = useShell();
  const { t } = useI18n("shell");
  const [password, setPassword] = createSignal("");
  const [error, setError] = createSignal<string>();
  const [busy, setBusy] = createSignal(false);
  let input: HTMLInputElement | undefined;

  // Every way out (button, Escape, backdrop) has to answer the pending requestAdmin(), or it never settles. That
  // includes the time the password is being checked: the API client has no timeout, so a hanging request must not
  // trap the user here. The check may still succeed afterwards, see submit().
  let cancelled = false;
  const cancel = () => {
    cancelled = true;
    shell.session.answerAdminRequest(false);
  };
  const onKeyDown = (e: KeyboardEvent) => {
    if (e.key === "Escape") cancel();
  };
  onMount(() => {
    input?.focus();
    document.addEventListener("keydown", onKeyDown);
  });
  onCleanup(() => document.removeEventListener("keydown", onKeyDown));

  const submit = async (e: SubmitEvent) => {
    e.preventDefault();
    if (busy()) return;
    setBusy(true);
    setError(undefined);
    try {
      await shell.session.enableAdmin(password());
      // Cancelled meanwhile, yet the server accepted the password: admin mode must not turn on against the user's
      // explicit "no". Whoever asked has been told false already. A failure here is not worth interrupting anyone.
      if (cancelled) void shell.session.disableAdmin().catch(() => {});
    } catch (err) {
      setError(adminError(err, t));
      setPassword("");
    } finally {
      setBusy(false);
    }
  };

  return (
    <div class="modal modal-open" role="dialog" aria-modal="true" aria-label={t("admin.title")}>
      <form class="modal-box flex flex-col gap-4" onSubmit={(e) => void submit(e)}>
        <h3 class="text-lg font-bold">{t("admin.title")}</h3>
        <p class="text-sm">{t("admin.message")}</p>
        <Show when={error()}>
          <div role="alert" class="alert alert-error text-sm">
            {error()}
          </div>
        </Show>
        <label class="flex flex-col gap-1">
          <span class="text-sm">{t("admin.password")}</span>
          <input
            ref={input}
            type="password"
            class="input w-full"
            autocomplete="current-password"
            required
            value={password()}
            onInput={(e) => setPassword(e.currentTarget.value)}
          />
        </label>
        <div class="modal-action">
          <button type="button" class="btn" onClick={cancel}>
            {t("admin.cancel")}
          </button>
          <button type="submit" class="btn btn-warning" disabled={busy()}>
            {t("admin.submit")}
          </button>
        </div>
      </form>
      <div class="modal-backdrop" aria-hidden="true" onClick={cancel} />
    </div>
  );
}

/** Password prompt answering session.requestAdmin(). */
export function AdminDialog() {
  const shell = useShell();
  return (
    <Show when={shell.session.adminRequested()}>
      <AdminPrompt />
    </Show>
  );
}
