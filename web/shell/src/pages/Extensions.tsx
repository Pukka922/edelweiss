import {
  ApiError,
  ConfirmDialog,
  ErrorAlert,
  type ExtensionInfo,
  localized,
  PageHeader,
  useI18n,
} from "@edelweiss/sdk";
import { batch, createEffect, createSignal, For, on, Show } from "solid-js";
import { InstallDialog, installerMessage } from "../components/InstallDialog";
import { UpdateDialog, type UpdateTarget } from "../components/UpdateDialog";
import { useShell } from "../context";
import { apkAddCommand, installerErrorKey, type Update } from "../state/installer";

function sourceText(ext: ExtensionInfo): string {
  const s = ext.source;
  return s.type === "github"
    ? `github: ${s.repo ?? s.location}${s.tag ? ` @ ${s.tag}` : ""}`
    : `${s.type}: ${s.location}`;
}

export function ExtensionsPage() {
  const shell = useShell();
  const { t } = useI18n("shell");
  const [removing, setRemoving] = createSignal<ExtensionInfo>();
  // The update being confirmed; it stays set, and its dialog open, while updatingId says that it is running, and
  // after a refusal, whose message the dialog shows until the user cancels or tries again.
  const [updating, setUpdating] = createSignal<UpdateTarget>();
  const [updatingId, setUpdatingId] = createSignal<string>();
  const [updateError, setUpdateError] = createSignal<string>();
  const closeUpdate = () =>
    batch(() => {
      setUpdating(undefined);
      setUpdateError(undefined);
    });
  const [installing, setInstalling] = createSignal(false);
  const [updates, setUpdates] = createSignal<Record<string, Update>>({});
  const [checking, setChecking] = createSignal(false);
  const name = (e: ExtensionInfo | undefined) => localized(e?.name, shell.i18n.locale());

  // Changing extensions needs admin mode: offered to those in it and to those who may enter it, not to everybody else.
  const mayChange = () => shell.session.admin() || shell.session.canAdmin();
  const ensureAdmin = async () => shell.session.admin() || (await shell.session.requestAdmin());

  // Bumped by every check and when admin mode ends: only the answer to the latest check is applied, and not at all
  // once admin mode is gone (answers can arrive in any order, and a list from an ended admin mode must not come back).
  let generation = 0;
  const checkUpdates = async () => {
    const mine = ++generation;
    const current = () => mine === generation && shell.session.admin();
    setChecking(true);
    try {
      const res = await shell.installer.updates();
      if (!current()) return;
      setUpdates(Object.fromEntries(res.updates.map((u) => [u.id, u])));
      for (const e of res.errors) {
        const message = t(installerErrorKey(e.code), { message: e.message });
        shell.toasts.error(t("extensions.updateCheckFailed", { id: e.id, message }));
      }
    } catch (err) {
      if (current()) shell.toasts.error(t("extensions.updatesFailed", { message: installerMessage(err, t) }));
    } finally {
      if (mine === generation) setChecking(false);
    }
  };
  // Update checks need admin mode: run one whenever it turns on; when it ends, forget the results, abandon a check
  // that is under way and close a confirmation that has not started yet.
  createEffect(
    on(
      () => shell.session.admin(),
      (admin) => {
        if (admin) {
          void checkUpdates();
          return;
        }
        generation++;
        batch(() => {
          setChecking(false);
          setUpdates({});
          if (updatingId() === undefined) closeUpdate();
        });
      },
    ),
  );
  const askCheck = () => {
    if (shell.session.admin()) void checkUpdates();
    else void shell.session.requestAdmin(); // the effect above checks once admin mode is on
  };

  const askInstall = async () => {
    if (await ensureAdmin()) setInstalling(true);
  };

  const askUpdate = async (ext: ExtensionInfo, update: Update) => {
    if (!(await ensureAdmin())) return;
    batch(() => {
      setUpdateError(undefined);
      setUpdating({ ext, update });
    });
  };
  const confirmUpdate = async () => {
    const u = updating();
    if (!u || updatingId() !== undefined) return;
    batch(() => {
      setUpdateError(undefined);
      setUpdatingId(u.ext.id);
    });
    try {
      // the checksum the user confirmed goes along: the core refuses if the latest release has another one
      const ext = await shell.installer.update(u.ext.id, u.update.sha256);
      await shell.extensions.reload();
      batch(() => {
        closeUpdate();
        setUpdatingId(undefined);
      });
      shell.toasts.success(t("extensions.updated", { name: name(ext), version: ext.version }));
    } catch (err) {
      // a refusal is told in the dialog, which stays open: a toast is gone after a few seconds, and the list has
      // nothing to show for it (the installed version needs nothing), so the user could not read what to do
      batch(() => {
        setUpdateError(installerMessage(err, t));
        setUpdatingId(undefined);
      });
    }
    // The entry the user confirmed is outdated now, whichever way it went: after a refusal (the release changed)
    // it still carries the old checksum, so look at the releases as they are.
    await checkUpdates();
  };

  const askRemove = async (ext: ExtensionInfo) => {
    if (await ensureAdmin()) setRemoving(ext);
  };
  const confirmRemove = async () => {
    const ext = removing();
    setRemoving(undefined);
    if (!ext) return;
    try {
      await shell.extensions.remove(ext.id);
      shell.toasts.success(t("extensions.removed", { name: name(ext) }));
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      // data_not_removed: the extension is gone, only its data directory could not be deleted; the answer says so and
      // names the directory, "Could not remove …: the extension was removed" would contradict itself
      shell.toasts.error(
        err instanceof ApiError && err.code === "data_not_removed"
          ? message
          : t("extensions.removeFailed", { name: name(ext), message }),
      );
    }
  };

  return (
    <section>
      <PageHeader
        title={t("extensions.title")}
        subtitle={t("extensions.subtitle")}
        actions={
          <Show when={mayChange()}>
            <button type="button" class="btn btn-sm" disabled={checking()} onClick={askCheck}>
              {checking() ? t("extensions.checking") : t("extensions.checkUpdates")}
            </button>
            <button type="button" class="btn btn-sm btn-primary" onClick={() => void askInstall()}>
              {t("extensions.install")}
            </button>
          </Show>
        }
      />
      <Show when={shell.extensions.error()}>
        {(err) => <ErrorAlert error={err()} onRetry={() => void shell.extensions.reload()} />}
      </Show>
      <Show
        when={shell.extensions.list().length > 0}
        fallback={<p class="text-base-content/70">{t("extensions.empty")}</p>}
      >
        <div class="overflow-x-auto">
          <table class="table">
            <thead>
              <tr>
                <th>{t("extensions.name")}</th>
                <th>{t("extensions.id")}</th>
                <th>{t("extensions.version")}</th>
                <th>{t("extensions.source")}</th>
                <th>
                  <span class="sr-only">{t("extensions.remove")}</span>
                </th>
              </tr>
            </thead>
            <tbody>
              <For each={shell.extensions.list()}>
                {(ext) => (
                  <tr>
                    <td>
                      <div class="flex items-center gap-2">
                        {name(ext)}
                        <Show when={ext.dev}>
                          <span class="badge badge-info badge-sm">{t("extensions.dev")}</span>
                        </Show>
                        <Show when={ext.requiresAdmin}>
                          <span class="badge badge-warning badge-sm">{t("extensions.adminOnly")}</span>
                        </Show>
                      </div>
                      <Show when={ext.missingPackages.length > 0}>
                        {/* a solid alert: its own text colour is readable on both themes, unlike text-warning */}
                        <div class="alert alert-warning mt-1 px-3 py-1 text-sm">
                          <p>
                            {t("extensions.missingPackages", { packages: ext.missingPackages.join(", ") })}{" "}
                            <code>{apkAddCommand(ext.missingPackages)}</code>
                          </p>
                        </div>
                      </Show>
                    </td>
                    <td>
                      <code>{ext.id}</code>
                    </td>
                    <td>{ext.version}</td>
                    <td class="max-w-xs truncate" title={ext.source.location}>
                      {sourceText(ext)}
                    </td>
                    <td>
                      <div class="flex justify-end gap-1">
                        <Show when={updates()[ext.id]}>
                          {(u) => (
                            <button
                              type="button"
                              class="btn btn-sm btn-primary"
                              disabled={updatingId() === ext.id}
                              onClick={() => void askUpdate(ext, u())}
                            >
                              {t("extensions.updateTo", { version: u().available })}
                            </button>
                          )}
                        </Show>
                        <button
                          type="button"
                          class="btn btn-sm btn-ghost text-error"
                          disabled={ext.dev || !mayChange() || updatingId() === ext.id}
                          onClick={() => void askRemove(ext)}
                        >
                          {t("extensions.remove")}
                        </button>
                      </div>
                    </td>
                  </tr>
                )}
              </For>
            </tbody>
          </table>
        </div>
      </Show>
      <InstallDialog
        open={installing()}
        onClose={() => setInstalling(false)}
        // the release just installed may not be the newest one: look for its update right away (admin mode only)
        onInstalled={() => {
          if (shell.session.admin()) void checkUpdates();
        }}
      />
      <UpdateDialog
        target={updating()}
        busy={updatingId() !== undefined}
        error={updateError()}
        onCancel={closeUpdate}
        onConfirm={() => void confirmUpdate()}
      />
      <ConfirmDialog
        open={removing() !== undefined}
        danger
        title={t("extensions.removeTitle")}
        message={t("extensions.removeMessage", { name: name(removing()) })}
        confirmLabel={t("extensions.remove")}
        onCancel={() => setRemoving(undefined)}
        onConfirm={() => void confirmRemove()}
      />
    </section>
  );
}
