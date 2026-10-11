import { ApiError, localized, type Translator, useI18n } from "@edelweiss/sdk";
import { createEffect, createSignal, For, on, onCleanup, onMount, Show } from "solid-js";
import { useShell } from "../context";
import {
  apkAddCommand,
  type Candidate,
  installerErrorKey,
  missingPackagesOf,
  type Resolution,
} from "../state/installer";

export function installerMessage(err: unknown, t: Translator["t"]): string {
  if (!(err instanceof ApiError)) return String(err);
  const packages = err.code === "missing_packages" ? missingPackagesOf(err) : [];
  if (packages.length > 0) {
    return t("installer.errors.missing_packages", {
      packages: packages.join(", "),
      command: apkAddCommand(packages),
    });
  }
  return t(installerErrorKey(err.code), { message: err.message });
}

function formatSize(bytes: number, locale: string): string {
  const mib = bytes >= 1024 * 1024;
  const value = mib ? bytes / 1024 / 1024 : bytes / 1024;
  return `${new Intl.NumberFormat(locale, { maximumFractionDigits: 1, useGrouping: false }).format(value)} ${mib ? "MiB" : "KiB"}`;
}

/** The form itself: it only exists while the dialog is open, so every opening starts clean. */
function InstallForm(props: { onClose: () => void; onInstalled?: () => void }) {
  const shell = useShell();
  const { t, locale } = useI18n("shell");
  const [source, setSource] = createSignal("");
  const [resolution, setResolution] = createSignal<Resolution>();
  const [chosen, setChosen] = createSignal<Candidate>();
  const [busy, setBusy] = createSignal<"resolving" | "installing">();
  const [error, setError] = createSignal<string>();
  let input: HTMLInputElement | undefined;

  // An install that is running finishes first: the server keeps going even if the dialog closes.
  const close = () => {
    if (busy() !== "installing") props.onClose();
  };
  const onKeyDown = (e: KeyboardEvent) => {
    if (e.key === "Escape") close();
  };
  onMount(() => {
    input?.focus();
    document.addEventListener("keydown", onKeyDown);
  });
  onCleanup(() => document.removeEventListener("keydown", onKeyDown));
  // Installing needs admin mode: when it ends under the dialog, the dialog goes too (unless an install is running).
  createEffect(
    on(
      () => shell.session.admin(),
      (admin) => {
        if (!admin) close();
      },
      { defer: true },
    ),
  );

  const find = async (e: SubmitEvent) => {
    e.preventDefault();
    if (busy()) return;
    setBusy("resolving");
    setError(undefined);
    try {
      const res = await shell.installer.resolve(source().trim());
      setResolution(res);
      setChosen(res.candidates[0]);
    } catch (err) {
      setError(installerMessage(err, t));
    } finally {
      setBusy(undefined);
    }
  };

  const install = async () => {
    const res = resolution();
    const c = chosen();
    if (!res || !c || busy()) return;
    setBusy("installing");
    setError(undefined);
    try {
      // exactly the reviewed release and file: tag and checksum go along
      const ext = await shell.installer.install(`${res.repo}@${res.tag}`, c.id, c.sha256);
      await shell.extensions.reload();
      shell.toasts.success(
        t("installer.installed", { name: localized(ext.name, locale()), version: ext.version }),
      );
      setBusy(undefined);
      props.onInstalled?.();
      props.onClose();
    } catch (err) {
      setError(installerMessage(err, t));
      setBusy(undefined);
    }
  };

  return (
    <div class="modal modal-open" role="dialog" aria-modal="true" aria-label={t("installer.title")}>
      <div class="modal-box flex max-w-2xl flex-col gap-4">
        <h3 class="text-lg font-bold">{t("installer.title")}</h3>
        <Show when={error()}>
          {(message) => (
            <div role="alert" class="alert alert-error text-sm">
              {message()}
            </div>
          )}
        </Show>
        <Show
          when={resolution()}
          fallback={
            <form class="flex flex-col gap-2" onSubmit={(e) => void find(e)}>
              <label class="flex flex-col gap-1">
                <span class="text-sm">{t("installer.source")}</span>
                <input
                  ref={input}
                  class="input w-full font-mono"
                  placeholder="owner/repo"
                  required
                  value={source()}
                  onInput={(e) => setSource(e.currentTarget.value)}
                />
              </label>
              <p class="text-xs text-base-content/70">{t("installer.sourceHint")}</p>
              <div class="modal-action">
                <button type="button" class="btn" onClick={close}>
                  {t("installer.cancel")}
                </button>
                <button type="submit" class="btn btn-primary" disabled={busy() !== undefined}>
                  <Show when={busy() === "resolving"}>
                    <span class="loading loading-spinner loading-sm" />
                  </Show>
                  {t("installer.find")}
                </button>
              </div>
            </form>
          }
        >
          {(res) => (
            <div class="flex flex-col gap-3">
              <dl class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-sm">
                <dt class="text-base-content/60">{t("installer.repository")}</dt>
                <dd class="font-mono">{res().repo}</dd>
                <dt class="text-base-content/60">{t("installer.release")}</dt>
                <dd>
                  {res().tag} · {new Date(res().publishedAt).toLocaleDateString(locale())}
                </dd>
              </dl>
              <fieldset class="flex flex-col gap-2" disabled={busy() === "installing"}>
                <legend class="mb-1 text-sm">{t("installer.extension")}</legend>
                <For each={res().candidates}>
                  {(c) => (
                    <label class="flex cursor-pointer items-start gap-3 rounded-box border border-base-300 p-3">
                      <input
                        type="radio"
                        class="radio radio-sm mt-1"
                        name="candidate"
                        checked={chosen()?.id === c.id}
                        onChange={() => setChosen(c)}
                      />
                      <div class="min-w-0 flex-1 text-sm">
                        <div class="font-semibold">
                          {c.id} {c.version}
                        </div>
                        <div class="break-all text-base-content/70">
                          {c.asset} · {formatSize(c.size, locale())}
                        </div>
                        <div class="break-all font-mono text-xs">
                          {t("installer.sha256")}: {c.sha256}
                        </div>
                      </div>
                    </label>
                  )}
                </For>
              </fieldset>
              <div class="alert alert-warning text-sm">{t("installer.warning")}</div>
              <div class="modal-action">
                <button
                  type="button"
                  class="btn"
                  disabled={busy() === "installing"}
                  onClick={() => {
                    setResolution(undefined);
                    setError(undefined);
                  }}
                >
                  {t("installer.back")}
                </button>
                <button type="button" class="btn" disabled={busy() === "installing"} onClick={close}>
                  {t("installer.cancel")}
                </button>
                <button
                  type="button"
                  class="btn btn-primary"
                  disabled={busy() !== undefined || !chosen()}
                  onClick={() => void install()}
                >
                  <Show when={busy() === "installing"}>
                    <span class="loading loading-spinner loading-sm" />
                  </Show>
                  {busy() === "installing" ? t("installer.installing") : t("installer.install")}
                </button>
              </div>
            </div>
          )}
        </Show>
      </div>
      <div class="modal-backdrop" aria-hidden="true" onClick={close} />
    </div>
  );
}

/**
 * Install-from-GitHub dialog: look up a release, review it, install it. onInstalled runs after a successful install
 * (the extension list is reloaded by then).
 */
export function InstallDialog(props: { open: boolean; onClose: () => void; onInstalled?: () => void }) {
  return (
    <Show when={props.open}>
      <InstallForm onClose={props.onClose} onInstalled={props.onInstalled} />
    </Show>
  );
}
