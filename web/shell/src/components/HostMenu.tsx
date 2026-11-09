import { ConfirmDialog, useI18n } from "@edelweiss/sdk";
import { IconPower, IconRefresh } from "@tabler/icons-solidjs";
import { batch, createEffect, createSignal, onCleanup, Show } from "solid-js";
import { Portal } from "solid-js/web";
import { useShell } from "../context";
import { waitForRestart } from "../health";
import { closeDropdown } from "./dropdown";

type Action = "reboot" | "poweroff";

/**
 * The Host menu: restart and shut down. Everybody sees it; only members of the admin group can use it (greyed out for
 * the others), in admin mode (asked for first). After the action a page over everything says what happens; after a
 * restart it waits until edelweiss answers again and then calls onBack (default: the login page, the sessions are gone).
 * pollMs is the time between two checks of the host, probeMs how long one check may take (tests make both short).
 */
export function HostMenu(props: { pollMs?: number; probeMs?: number; onBack?: () => void }) {
  const shell = useShell();
  const { t } = useI18n("shell");
  const [asking, setAsking] = createSignal<Action>();
  const [busy, setBusy] = createSignal(false); // the request of the confirmed action is on its way
  const [done, setDone] = createSignal<Action>();
  const [stillUp, setStillUp] = createSignal(false);
  const host = () => shell.session.info()?.hostname ?? "";
  const pollMs = () => props.pollMs ?? 3000;
  const probeMs = () => props.probeMs ?? 5000;
  let menu: HTMLDetailsElement | undefined;
  let summary: HTMLElement | undefined;
  // Bumped to end what is going on after a confirmation: when the page is closed or this menu is gone, a request or
  // a poll that is on its way must not start the wait or schedule the next poll.
  let epoch = 0;
  const stopWaiting = () => {
    epoch++;
    waiting?.abort();
  };
  onCleanup(stopWaiting);

  // While the page over everything is up, the app behind it is inert: nothing there can be reached with the keyboard,
  // so no second action can be sent. The app is the element of the body that holds this menu (#app).
  createEffect(() => {
    if (!done()) return;
    let root = menu?.parentElement ?? null;
    while (root && root.parentElement !== document.body) root = root.parentElement;
    if (!root || root.hasAttribute("inert")) return;
    const app = root;
    app.setAttribute("inert", "");
    onCleanup(() => app.removeAttribute("inert"));
  });

  // The menu's entries hide the button that had the focus, and the dialogs do not give it back: a cancelled or failed
  // action leaves the focus on the menu's summary instead of the page.
  const focusSummary = () => summary?.focus();

  const choose = async (action: Action) => {
    if (busy()) return;
    if (!shell.session.admin() && !(await shell.session.requestAdmin())) {
      focusSummary();
      return;
    }
    setAsking(action);
  };

  // after a restart: wait until /api/health fails once (the host went down) and answers again. After two minutes of
  // an answering host the restart may have failed; the waiting goes on all the same (the host may still go down).
  let waiting: AbortController | undefined;
  const waitForHost = async (mine: number) => {
    waiting = new AbortController();
    const result = await waitForRestart({
      before: undefined,
      pollMs: pollMs(),
      probeMs: probeMs(),
      timeoutMs: 120_000,
      signal: waiting.signal,
    });
    if (mine !== epoch) return;
    if (result === "back") {
      (props.onBack ?? (() => window.location.assign("/login")))();
      return;
    }
    setStillUp(true);
    void waitForHost(mine);
  };

  const confirm = async () => {
    const action = asking();
    if (!action || busy()) return;
    const mine = epoch;
    setBusy(true);
    try {
      await shell.session.api.post(`/system/${action}`, {});
      if (mine !== epoch) return; // the menu is gone
      batch(() => {
        setAsking(undefined);
        setDone(action);
      });
      if (action === "reboot") void waitForHost(mine);
    } catch (err) {
      setAsking(undefined);
      focusSummary();
      shell.toasts.error(t("host.failed", { message: err instanceof Error ? err.message : String(err) }));
    } finally {
      setBusy(false);
    }
  };

  const cancel = () => {
    setAsking(undefined);
    focusSummary();
  };

  const closePage = () => {
    stopWaiting();
    setStillUp(false);
    setDone(undefined);
  };

  const item = (action: Action, Icon: typeof IconPower, label: string) => (
    <li class={shell.session.canAdmin() ? "" : "menu-disabled"}>
      <button
        type="button"
        disabled={!shell.session.canAdmin()}
        onClick={(e) => {
          closeDropdown(e.currentTarget); // the menu is out of the way before the dialog opens
          void choose(action);
        }}
      >
        <span aria-hidden="true">
          <Icon size={18} strokeWidth={1.75} />
        </span>
        {/* one cell for the label and the hint: the menu item is a grid with a column for each child */}
        <span class="flex flex-col items-start">
          <span class="whitespace-nowrap">{label}</span>
          <Show when={!shell.session.canAdmin()}>
            <span class="text-xs opacity-70">{t("host.adminOnly")}</span>
          </Show>
        </span>
      </button>
    </li>
  );

  return (
    <>
      <details ref={menu} class="dropdown dropdown-end">
        <summary ref={summary} class="btn btn-sm btn-ghost">
          <span aria-hidden="true">
            <IconPower size={18} strokeWidth={1.75} />
          </span>
          <span class="shell-label">{t("host.menu")}</span>
        </summary>
        <ul class="menu dropdown-content z-20 mt-2 w-64 rounded-box bg-base-200 p-2 shadow">
          {item("reboot", IconRefresh, t("host.reboot"))}
          {item("poweroff", IconPower, t("host.poweroff"))}
        </ul>
      </details>
      {/* In the body, not in the navbar: the sticky navbar is a stacking context below the sidebar (z-20), and the
          dialog and the page over everything must cover the sidebar as well. */}
      <Portal>
        <ConfirmDialog
          open={asking() !== undefined}
          title={asking() === "poweroff" ? t("host.poweroffTitle") : t("host.rebootTitle")}
          message={
            asking() === "poweroff"
              ? t("host.poweroffMessage", { name: host() })
              : t("host.rebootMessage", { name: host() })
          }
          confirmLabel={asking() === "poweroff" ? t("host.poweroff") : t("host.reboot")}
          danger
          busy={busy()}
          onConfirm={() => void confirm()}
          onCancel={cancel}
        />
        <Show when={done()}>
          {(action) => (
            <div
              class="fixed inset-0 z-50 flex flex-col items-center justify-center gap-4 bg-base-100 p-6 text-center"
              role="status"
            >
              <Show
                when={action() === "reboot"}
                fallback={<p class="text-lg">{t("host.poweredOff", { name: host() })}</p>}
              >
                <span class="loading loading-spinner loading-lg" aria-hidden="true" />
                <p class="text-lg">{t("host.rebooting", { name: host() })}</p>
                <Show when={stillUp()}>
                  <p class="text-sm text-warning">{t("host.stillUp")}</p>
                  <button type="button" class="btn btn-sm" onClick={closePage}>
                    {t("host.close")}
                  </button>
                </Show>
              </Show>
            </div>
          )}
        </Show>
      </Portal>
    </>
  );
}
