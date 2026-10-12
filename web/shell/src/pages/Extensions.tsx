import {
  ApiError,
  ConfirmDialog,
  ErrorAlert,
  type ExtensionInfo,
  localized,
  PageHeader,
  useI18n,
} from "@edelweiss/sdk";
import { IconTrash } from "@tabler/icons-solidjs";
import { batch, createEffect, createSignal, For, on, onCleanup, Show } from "solid-js";
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
  const { t, locale } = useI18n("shell");
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
  // The result on show: when it was found, how old it was by the clock of the core when the answer arrived, and when
  // that was by the clock of the browser (a browser clock that is wrong must not make the age wrong: only the time
  // since the answer is measured here). And whether GitHub failed to answer since, so that it is the last good result.
  const [checked, setChecked] = createSignal<{ at: Date; ageMs: number; receivedAt: number }>();
  const [stale, setStale] = createSignal(false);
  const [staleCode, setStaleCode] = createSignal("");
  const [staleReason, setStaleReason] = createSignal("");
  // The clock of the age of the check; it ticks while the page is open.
  const [now, setNow] = createSignal(Date.now());
  const tick = setInterval(() => setNow(Date.now()), 15_000);
  onCleanup(() => clearInterval(tick));
  const checkedText = () => {
    const c = checked();
    if (!c) return "";
    const minutes = Math.floor(Math.max(0, c.ageMs + now() - c.receivedAt) / 60_000);
    if (minutes < 1) return t("extensions.checkedJustNow");
    // A kept result can be old (GitHub does not answer for an hour or more): the unit follows the size of the age, and
    // the formatter says it in the words and plural forms of the language ("1 hour ago", "yesterday", "vor 3 Tagen").
    const [count, unit]: [number, Intl.RelativeTimeFormatUnit] =
      minutes < 60
        ? [minutes, "minute"]
        : minutes < 24 * 60
          ? [Math.floor(minutes / 60), "hour"]
          : [Math.floor(minutes / (24 * 60)), "day"];
    const time = new Intl.RelativeTimeFormat(locale(), { numeric: "auto" }).format(-count, unit);
    return t("extensions.checkedAgo", { time });
  };
  const staleText = (at: Date) =>
    t("extensions.updatesStale", {
      // a code the shell knows has its own text; for any other, the message of the core is the reason
      reason: t(installerErrorKey(staleCode()), { message: staleReason() }).replace(/\.$/, ""),
      // with the date: the kept result may be from another day
      time: new Intl.DateTimeFormat(locale(), { dateStyle: "short", timeStyle: "short" }).format(at),
    });
  const name = (e: ExtensionInfo | undefined) => localized(e?.name, shell.i18n.locale());

  // Changing extensions needs admin mode: offered to those in it and to those who may enter it, not to everybody else.
  const mayChange = () => shell.session.admin() || shell.session.canAdmin();
  const ensureAdmin = async () => shell.session.admin() || (await shell.session.requestAdmin());

  // Bumped by every check and when admin mode ends: only the answer to the latest check is applied, and not at all
  // once admin mode is gone (answers can arrive in any order, and a list from an ended admin mode must not come back).
  let generation = 0;
  // refresh: GitHub is asked, not only the cache of the core. It is for what the user asked for (the button) and for
  // a check after a refusal that the cache does not know about (the release changed).
  const checkUpdates = async (refresh = false) => {
    const mine = ++generation;
    const current = () => mine === generation && shell.session.admin();
    setChecking(true);
    try {
      const res = await shell.installer.updates(refresh);
      if (!current()) return;
      const at = new Date(res.checkedAt);
      const receivedAt = Date.now();
      batch(() => {
        setUpdates(Object.fromEntries(res.updates.map((u) => [u.id, u])));
        setChecked(
          Number.isNaN(at.getTime()) || !Number.isFinite(res.ageSeconds)
            ? undefined
            : { at, ageMs: res.ageSeconds * 1000, receivedAt },
        );
        setStale(res.stale);
        setStaleCode(res.staleCode ?? "");
        setStaleReason(res.staleReason ?? "");
        setNow(receivedAt);
      });
      // the core answers from its cache for 2 minutes, also when the page is visited again: the same check is not
      // told again
      if (shell.updateChecks.isNew(res.checkedAt)) {
        for (const e of res.errors) {
          const message = t(installerErrorKey(e.code), { message: e.message });
          shell.toasts.error(t("extensions.updateCheckFailed", { id: e.id, message }));
        }
      }
    } catch (err) {
      if (current()) shell.toasts.error(t("extensions.updatesFailed", { message: installerMessage(err, t) }));
    } finally {
      if (mine === generation) setChecking(false);
    }
  };
  // The button was pressed outside admin mode: the check that follows once admin mode is on is the button's.
  let refreshNext = false;
  // Update checks need admin mode: run one whenever it turns on; when it ends, forget the results, abandon a check
  // that is under way and close a confirmation that has not started yet.
  createEffect(
    on(
      () => shell.session.admin(),
      (admin) => {
        if (admin) {
          const refresh = refreshNext;
          refreshNext = false;
          void checkUpdates(refresh);
          return;
        }
        generation++;
        batch(() => {
          setChecking(false);
          setUpdates({});
          setChecked(undefined);
          setStale(false);
          if (updatingId() === undefined) closeUpdate();
        });
      },
    ),
  );
  const askCheck = () => {
    if (shell.session.admin()) {
      void checkUpdates(true);
      return;
    }
    refreshNext = true; // the effect above checks once admin mode is on
    void shell.session.requestAdmin().then((granted) => {
      if (!granted) refreshNext = false; // declined: a later admin mode, turned on elsewhere, is not the button's
    });
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
    let refused = false;
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
      refused = true;
      batch(() => {
        setUpdateError(installerMessage(err, t));
        setUpdatingId(undefined);
      });
    }
    // The entry the user confirmed is outdated now, whichever way it went. After an update the core's cache does not
    // apply any more (the version changed). After a refusal (the release changed) it does, and still holds the entry
    // with the old checksum: look at the releases as they are, on GitHub.
    await checkUpdates(refused);
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
            <Show when={checked()}>
              <span class="self-center text-sm text-base-content/70">{checkedText()}</span>
            </Show>
            <button type="button" class="btn btn-sm" disabled={checking()} onClick={askCheck}>
              {checking() ? t("extensions.checking") : t("extensions.checkUpdates")}
            </button>
            <button type="button" class="btn btn-sm btn-primary" onClick={() => void askInstall()}>
              {t("extensions.install")}
            </button>
          </Show>
        }
      />
      <Show when={stale() && checked()}>
        {(c) => (
          <div role="status" class="alert alert-warning mb-4 text-sm">
            <p>{staleText(c().at)}</p>
          </div>
        )}
      </Show>
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
                        <div
                          class="tooltip tooltip-left before:max-w-none before:whitespace-nowrap"
                          data-tip={t("extensions.remove")}
                        >
                          <button
                            type="button"
                            class="btn btn-ghost btn-sm btn-square text-error disabled:opacity-40"
                            aria-label={`${t("extensions.remove")} ${name(ext)}`}
                            disabled={ext.dev || !mayChange() || updatingId() === ext.id}
                            onClick={() => void askRemove(ext)}
                          >
                            <span aria-hidden="true">
                              <IconTrash size={18} strokeWidth={1.75} />
                            </span>
                          </button>
                        </div>
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
