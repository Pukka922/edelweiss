import { ApiError, ErrorAlert, PageHeader, useI18n } from "@edelweiss/sdk";
import { batch, createEffect, createMemo, createSignal, on, onCleanup, onMount, Show } from "solid-js";
import { Portal } from "solid-js/web";
import {
  type AboutInfo,
  aboutMessage,
  type CheckView,
  DEV_VERSION,
  outcomeKey,
  reasonText,
  resultReasonText,
} from "../about";
import { SelfUpdateDialog } from "../components/SelfUpdateDialog";
import { useShell } from "../context";
import { readHealth, waitForRestart } from "../health";

type Phase = "idle" | "starting" | "restarting" | "timeout";

/**
 * Settings, About: the installed version and, in admin mode, the check for a newer release and the self-update.
 * pollMs is the time between two looks at a running update and between two probes of the restarting host, probeMs how
 * long one probe may take, timeoutMs how long the page waits for edelweiss after the restart (default 5 minutes: the
 * helper needs up to about four and a half); onBack is where the page goes once edelweiss runs again (default: the
 * login page, the sessions are gone). Tests make the times short.
 */
export function AboutPage(props: {
  pollMs?: number;
  probeMs?: number;
  timeoutMs?: number;
  onBack?: () => void;
}) {
  const shell = useShell();
  const { t, locale } = useI18n("shell");
  const pollMs = () => props.pollMs ?? 3000;

  const [info, setInfo] = createSignal<AboutInfo>();
  const [infoError, setInfoError] = createSignal<unknown>();
  const reloadInfo = async () => {
    try {
      const res = await shell.about.info();
      batch(() => {
        setInfo(res);
        setInfoError(undefined);
      });
    } catch (err) {
      // a failed look while an update runs is the restart: only the first load needs to tell
      if (!info()) setInfoError(err);
    }
  };
  onMount(() => void reloadInfo());
  // The answer depends on admin mode (the raw message of a failed lbu commit is for admins): after the update everybody
  // signs in again without it, so the page asks again when it is turned on.
  createEffect(
    on(
      () => shell.session.admin(),
      (on, was) => {
        // only when it turns on: the session info is replaced on every refresh too, and a reload then would clear the
        // dialog while an update starts
        if (on && was === false) void reloadInfo();
      },
      { defer: true },
    ),
  );

  const running = createMemo(() => info()?.state.running === true);
  const isDev = () => info()?.version === DEV_VERSION;
  // The update area exists where self-update can work at all; elsewhere the reason takes its place.
  const possible = () => info()?.possible === true && !isDev();

  // While an update runs, look at its state until it ends: its result is shown then.
  createEffect(() => {
    if (!running()) return;
    let stopped = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const poll = () => {
      timer = setTimeout(async () => {
        await reloadInfo();
        if (!stopped) poll();
      }, pollMs());
    };
    poll();
    onCleanup(() => {
      stopped = true;
      clearTimeout(timer);
    });
  });

  // --- the check ---------------------------------------------------------------------------------------------------
  const [checkView, setCheckView] = createSignal<CheckView>();
  const [checking, setChecking] = createSignal(false);
  const [checkError, setCheckError] = createSignal<string>();
  // when the answer arrived by the clock of the browser: the age is measured from the core's clock, not the browser's
  const [receivedAt, setReceivedAt] = createSignal(0);
  const [now, setNow] = createSignal(Date.now());
  const tick = setInterval(() => setNow(Date.now()), 15_000);
  onCleanup(() => clearInterval(tick));

  const mayChange = () => shell.session.admin() || shell.session.canAdmin();
  const ensureAdmin = async () => shell.session.admin() || (await shell.session.requestAdmin());

  // Bumped by every check and when the check is not wanted any more: only the answer to the latest one is applied.
  let generation = 0;
  const checkUpdates = async (refresh = false) => {
    const mine = ++generation;
    const current = () => mine === generation && shell.session.admin();
    setChecking(true);
    try {
      const res = await shell.about.check(refresh);
      if (!current()) return;
      batch(() => {
        setCheckView(res);
        setCheckError(undefined);
        setReceivedAt(Date.now());
        setNow(Date.now());
      });
    } catch (err) {
      if (current()) setCheckError(aboutMessage(err, t));
    } finally {
      if (mine === generation) setChecking(false);
    }
  };

  // a memo: the check is wanted when this turns true, not each time the answer of info() is replaced
  const checkable = createMemo(() => shell.session.admin() && possible() && !running());
  // The button was pressed outside admin mode: the check that follows once admin mode is on is the button's.
  let refreshNext = false;
  createEffect(
    on(checkable, (yes) => {
      if (yes) {
        const refresh = refreshNext;
        refreshNext = false;
        void checkUpdates(refresh);
        return;
      }
      generation++;
      batch(() => {
        setChecking(false);
        setCheckView(undefined);
        setCheckError(undefined);
      });
    }),
  );
  const askCheck = () => {
    if (shell.session.admin()) {
      void checkUpdates(true);
      return;
    }
    refreshNext = true;
    void shell.session.requestAdmin().then((granted) => {
      if (!granted) refreshNext = false;
    });
  };

  const checkedText = () => {
    const c = checkView();
    if (!c || !Number.isFinite(c.ageSeconds)) return "";
    const minutes = Math.floor(Math.max(0, c.ageSeconds * 1000 + now() - receivedAt()) / 60_000);
    if (minutes < 1) return t("about.checkedJustNow");
    const [count, unit]: [number, Intl.RelativeTimeFormatUnit] =
      minutes < 60
        ? [minutes, "minute"]
        : minutes < 24 * 60
          ? [Math.floor(minutes / 60), "hour"]
          : [Math.floor(minutes / (24 * 60)), "day"];
    const time = new Intl.RelativeTimeFormat(locale(), { numeric: "auto" }).format(-count, unit);
    return t("about.checkedAgo", { time });
  };
  const formatDate = (iso: string | undefined, style: "date" | "datetime") => {
    const at = new Date(iso ?? "");
    if (Number.isNaN(at.getTime())) return iso ?? "";
    return new Intl.DateTimeFormat(
      locale(),
      style === "date" ? { dateStyle: "long" } : { dateStyle: "short", timeStyle: "short" },
    ).format(at);
  };
  // a short phrase for the codes the page knows; for any other, the message of the core is the reason
  const staleText = (c: CheckView) => {
    const code = c.staleCode ?? "";
    const known = ["github_rate_limited", "github_unavailable", "timeout"].includes(code);
    return t("about.staleShown", {
      reason: (known ? t(`about.staleReasons.${code}`) : (c.staleReason ?? code)).replace(/\.$/, ""),
      time: formatDate(c.checkedAt, "datetime"),
    });
  };

  // --- the update --------------------------------------------------------------------------------------------------
  const [confirming, setConfirming] = createSignal(false);
  const [phase, setPhase] = createSignal<Phase>("idle");
  const [updateError, setUpdateError] = createSignal<string>();
  // The old edelweiss answers although the update was started, and there is no result of it: nothing was done.
  const [notStarted, setNotStarted] = createSignal(false);
  let waiting: AbortController | undefined;
  onCleanup(() => waiting?.abort());

  // the dialog shows the offer as it is now: after a refusal that the release changed, the new check updates it
  const target = () => {
    const c = checkView();
    const i = info();
    if (!confirming() || !c || !i || !c.offer.available) return undefined;
    return { repo: i.repo, offer: c.offer, lbuWarning: c.lbuWarning };
  };

  const dialogOpen = () => confirming() && target() !== undefined;
  // The offer went away while the dialog was open (the release changed, admin mode ended): it closes for good, and a
  // later offer does not open it again by itself. The refusal stays on the page.
  createEffect(() => {
    if (confirming() && phase() === "idle" && target() === undefined) setConfirming(false);
  });
  let updateButton: HTMLButtonElement | undefined;
  createEffect(
    on(confirming, (now, before) => {
      // the dialog closed: the focus goes back to where it was opened (the page is no longer inert by then)
      if (before && !now && phase() === "idle") queueMicrotask(() => updateButton?.focus());
    }),
  );

  const askUpdate = async () => {
    if (!(await ensureAdmin())) return;
    batch(() => {
      setUpdateError(undefined);
      setConfirming(true);
    });
  };
  const closeDialog = () =>
    batch(() => {
      setConfirming(false);
      setUpdateError(undefined);
      setNotStarted(false);
    });

  const goBack = () => (props.onBack ?? (() => window.location.assign("/login?next=/settings/about")))();
  // While the page waits for the restart, the state is looked at too. Sessions live in the memory of the process, so
  // an answer from the old edelweiss (not a 401 from the new one) means that no restart happened: the helper ended
  // with "failed" or with a rollback that left the old process running, ended without a result, or never ran (the start
  // did not arrive). state.running is true from the start of the update to the end of the helper, without a gap, so an
  // answer with running false is the end. A result with another time than before is that of this update; without one
  // nothing was started. Whatever ended it, there is no restart to wait for.
  const watchResult = (mine: AbortController, previous: string | undefined) => {
    let timer: ReturnType<typeof setTimeout> | undefined;
    mine.signal.addEventListener("abort", () => clearTimeout(timer), { once: true });
    const look = () => {
      timer = setTimeout(async () => {
        try {
          const res = await shell.about.info();
          if (mine.signal.aborted) return;
          if (res.state.running === false) {
            mine.abort();
            batch(() => {
              setInfo(res);
              setNotStarted(res.state.result === undefined || res.state.result.at === previous);
              setPhase("idle");
            });
            return;
          }
        } catch {
          // the host is restarting: no answer is expected
        }
        if (!mine.signal.aborted) look();
      }, pollMs());
    };
    look();
  };

  const confirmUpdate = async () => {
    const sha = target()?.offer.sha256;
    if (!sha || phase() !== "idle") return;
    const previous = info()?.state.result?.at;
    batch(() => {
      setUpdateError(undefined);
      setNotStarted(false);
      setPhase("starting");
    });
    // what the running edelweissd calls its start, to tell the new one from it; a health check that hangs must not
    // hold the update back: without it the restart is seen as down and up again
    const limit = new AbortController();
    const cut = setTimeout(() => limit.abort(), props.probeMs ?? 5000);
    const before = (await readHealth(limit.signal))?.started;
    clearTimeout(cut);
    try {
      await shell.about.start(sha);
    } catch (err) {
      if (err instanceof ApiError) {
        batch(() => {
          setUpdateError(aboutMessage(err, t));
          setPhase("idle");
        });
        // the offer may be outdated: look at the release as it is on GitHub
        void checkUpdates(true);
        return;
      }
      // The answer is lost (the network failed): the update may well be running. Nothing is known, so no error is
      // shown; the wait for the restart tells.
    }
    batch(() => {
      setConfirming(false);
      setPhase("restarting");
    });
    const mine = new AbortController();
    waiting?.abort();
    waiting = mine;
    watchResult(mine, previous);
    const result = await waitForRestart({
      before,
      pollMs: props.pollMs,
      probeMs: props.probeMs,
      timeoutMs: props.timeoutMs ?? 300_000,
      signal: mine.signal,
    });
    if (mine.signal.aborted) return;
    if (result === "back") goBack();
    else setPhase("timeout");
  };
  const closeOverlay = () => {
    waiting?.abort();
    batch(() => {
      setNotStarted(false);
      setPhase("idle");
    });
    void reloadInfo();
  };

  const dismiss = async () => {
    if (!(await ensureAdmin())) return;
    try {
      await shell.about.dismiss();
      await reloadInfo();
    } catch (err) {
      shell.toasts.error(t("about.dismissFailed", { message: aboutMessage(err, t) }));
    }
  };

  // While the dialog or the page over everything is up, the app behind it is inert: nothing there can be reached
  // with the keyboard, so no second update can be started. The app is the element of the body that holds this page.
  let section: HTMLElement | undefined;
  createEffect(() => {
    if (phase() !== "restarting" && phase() !== "timeout" && !dialogOpen()) return;
    let root = section?.parentElement ?? null;
    while (root && root.parentElement !== document.body) root = root.parentElement;
    if (!root || root.hasAttribute("inert")) return;
    const app = root;
    app.setAttribute("inert", "");
    onCleanup(() => app.removeAttribute("inert"));
  });

  // --- the pieces --------------------------------------------------------------------------------------------------
  const resultNote = (r: NonNullable<AboutInfo["state"]["result"]>) => {
    const key = outcomeKey(r.outcome);
    const params = { from: r.from, to: r.to, time: formatDate(r.at, "datetime"), outcome: r.outcome };
    return (
      <div
        role="alert"
        class={`alert mb-4 items-start text-sm ${r.outcome === "updated" ? "alert-success" : "alert-warning"}`}
      >
        <div class="flex min-w-0 flex-1 flex-col gap-1">
          <p>{key ? t(key, params) : t("about.outcomes.unknown", params)}</p>
          <Show when={r.reason}>
            {(reason) => <p>{t("about.reason", { reason: resultReasonText(reason(), t) })}</p>}
          </Show>
          <Show when={r.outcome === "rollback_failed"}>
            <p>{t("about.syslogHint")}</p>
          </Show>
          <Show when={r.lbuFailed}>
            <p>{t("about.lbuFailed")}</p>
            <Show when={r.lbuError}>
              {(message) => <p class="break-words">{t("about.lbuDetail", { message: message() })}</p>}
            </Show>
          </Show>
        </div>
        <Show when={mayChange()}>
          <button type="button" class="btn btn-sm" onClick={() => void dismiss()}>
            {t("about.close")}
          </button>
        </Show>
      </div>
    );
  };

  const updateArea = () => (
    <Show
      when={!running()}
      fallback={
        <p role="status" class="flex items-center gap-2">
          <span class="loading loading-spinner loading-sm" aria-hidden="true" />
          {t("about.running")}
        </p>
      }
    >
      <Show
        when={shell.session.admin()}
        fallback={
          <Show when={mayChange()} fallback={<p class="text-base-content/70">{t("about.adminOnly")}</p>}>
            <div>
              <button type="button" class="btn btn-sm" onClick={askCheck}>
                {t("about.checkUpdates")}
              </button>
            </div>
          </Show>
        }
      >
        <Show when={checkError()}>
          {(message) => (
            <div role="alert" class="alert alert-error mb-3 text-sm">
              {message()}
            </div>
          )}
        </Show>
        <Show when={checkView()}>
          {(c) => (
            <>
              <Show when={c().stale}>
                <div role="status" class="alert alert-warning mb-3 text-sm">
                  <div>
                    <p class="font-medium">{t("about.staleTitle")}</p>
                    <p>{staleText(c())}</p>
                  </div>
                </div>
              </Show>
              <Show when={c().offer.available} fallback={<p class="font-medium">{t("about.upToDate")}</p>}>
                <p>
                  {t("about.available", {
                    version: c().offer.latest ?? c().offer.release ?? "",
                    date: formatDate(c().offer.published, "date"),
                  })}
                </p>
                <div class="mt-3 flex flex-wrap items-center gap-2">
                  <Show when={c().offer.notesUrl}>
                    {(url) => (
                      <a class="btn btn-sm btn-ghost" href={url()} target="_blank" rel="noopener noreferrer">
                        {t("about.notes")}
                      </a>
                    )}
                  </Show>
                  <button
                    ref={updateButton}
                    type="button"
                    class="btn btn-sm btn-primary"
                    onClick={() => void askUpdate()}
                  >
                    {t("about.update")}
                  </button>
                </div>
              </Show>
            </>
          )}
        </Show>
        <Show when={checking() && !checkView()}>
          <p class="flex items-center gap-2">
            <span class="loading loading-spinner loading-sm" aria-hidden="true" />
            {t("about.checking")}
          </p>
        </Show>
        <Show when={checkView()}>
          <div class="mt-3 flex flex-wrap items-center gap-2 text-sm">
            <span class="text-base-content/70">{checkedText()}</span>
            <button type="button" class="btn btn-sm" disabled={checking()} onClick={askCheck}>
              {checking() ? t("about.checking") : t("about.checkAgain")}
            </button>
          </div>
        </Show>
      </Show>
    </Show>
  );

  const notPossible = (i: AboutInfo) => (
    <div class="flex flex-col gap-1">
      <p>{t("about.notPossible")}</p>
      <Show when={i.reason}>{(reason) => <p class="text-base-content/70">{reasonText(reason(), t)}</p>}</Show>
      <p class="text-base-content/70">
        {t("about.readmeHint")}{" "}
        <a
          class="link"
          href={`https://github.com/${i.repo}#readme`}
          target="_blank"
          rel="noopener noreferrer"
        >
          README
        </a>
      </p>
    </div>
  );

  return (
    <section ref={section}>
      <PageHeader title={t("about.title")} subtitle={t("about.subtitle")} />
      <Show when={infoError()}>
        {(err) => <ErrorAlert error={err()} onRetry={() => void reloadInfo()} />}
      </Show>
      <Show
        when={info()}
        fallback={
          <Show when={!infoError()}>
            <p class="text-base-content/70">{t("about.loading")}</p>
          </Show>
        }
      >
        {(i) => (
          <>
            <h2 class="text-xl font-semibold">edelweiss</h2>
            <dl class="mb-6 mt-1 grid max-w-md grid-cols-[auto_1fr] gap-x-6 gap-y-1">
              <dt class="text-base-content/60">{t("about.version")}</dt>
              <dd class="break-all">{isDev() ? t("about.devVersion") : i().version}</dd>
              <dt class="text-base-content/60">{t("about.arch")}</dt>
              <dd>{i().arch}</dd>
              <dt class="text-base-content/60">{t("about.mode")}</dt>
              <dd>{i().mode}</dd>
            </dl>
            <Show when={updateError() && !confirming()}>
              <div role="alert" class="alert alert-error mb-4 text-sm">
                {updateError()}
              </div>
            </Show>
            <Show when={notStarted()}>
              <div role="alert" class="alert alert-warning mb-4 text-sm">
                {t("about.notStarted")}
              </div>
            </Show>
            {/* keyed: the note is drawn again when the answer is replaced (the raw lbu message comes with admin mode) */}
            <Show when={i().state.result} keyed>
              {(r) => resultNote(r)}
            </Show>
            <div class="mb-8 rounded-box border border-base-300 p-4">
              <h2 class="mb-3 text-lg font-semibold">{t("about.updates")}</h2>
              <Show when={possible()} fallback={notPossible(i())}>
                {updateArea()}
              </Show>
            </div>
            <footer class="flex flex-wrap gap-x-6 gap-y-1 text-sm">
              <a
                class="link"
                href={`https://github.com/${i().repo}`}
                target="_blank"
                rel="noopener noreferrer"
              >
                {t("about.repository")}
              </a>
              <a
                class="link"
                href={`https://github.com/${i().repo}/blob/main/LICENSE`}
                target="_blank"
                rel="noopener noreferrer"
              >
                {t("about.license")}
              </a>
            </footer>
          </>
        )}
      </Show>
      {/* In the body, not in the page: the dialog and the page over everything must cover the sidebar as well. */}
      <Portal>
        <SelfUpdateDialog
          target={phase() === "restarting" || phase() === "timeout" ? undefined : target()}
          busy={phase() === "starting"}
          progress={phase() === "starting" ? t("about.downloading") : undefined}
          error={updateError()}
          onCancel={closeDialog}
          onConfirm={() => void confirmUpdate()}
        />
        <Show when={phase() === "restarting" || phase() === "timeout"}>
          <div
            class="fixed inset-0 z-50 flex flex-col items-center justify-center gap-4 bg-base-100 p-6 text-center"
            role="status"
          >
            <Show
              when={phase() === "restarting"}
              fallback={
                <>
                  <p class="max-w-prose">{t("about.noAnswer")}</p>
                  <button type="button" class="btn btn-sm" onClick={closeOverlay}>
                    {t("about.close")}
                  </button>
                </>
              }
            >
              <span class="loading loading-spinner loading-lg" aria-hidden="true" />
              <p class="text-lg">{t("about.restarting")}</p>
            </Show>
          </div>
        </Show>
      </Portal>
    </section>
  );
}
