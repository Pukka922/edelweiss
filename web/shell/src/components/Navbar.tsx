import { ApiError, type Locale, useI18n } from "@edelweiss/sdk";
import { useNavigate } from "@solidjs/router";
import { IconLogout, IconUser } from "@tabler/icons-solidjs";
import { createUniqueId, Show } from "solid-js";
import { useShell } from "../context";
import type { Theme } from "../prefs";
import { useDropdownDismiss } from "./dropdown";
import { HostMenu } from "./HostMenu";
import { LockIcon, MenuIcon, UnlockIcon } from "./Icons";

/**
 * daisyUI's select may shrink in a flex row, below its content, and the chevron then covers "EN" or "Light". In the
 * user menu's rows (the text on the left, the select on the right) shrink-0 keeps the width the content and the
 * chevron need, on a phone as well as on desktop.
 */
const selectClass = "select select-sm w-auto shrink-0";

/** The id is the one a label points to (the rows of the user menu); the login page has no label and none. */
export function LanguageSelect(props: { id?: string }) {
  const shell = useShell();
  const { t } = useI18n("shell");
  return (
    <select
      id={props.id}
      class={selectClass}
      aria-label={t("navbar.language")}
      value={shell.i18n.locale()}
      onChange={(e) => shell.setLocale(e.currentTarget.value as Locale)}
    >
      <option value="de">DE</option>
      <option value="en">EN</option>
    </select>
  );
}

export function ThemeSelect(props: { id?: string }) {
  const shell = useShell();
  const { t } = useI18n("shell");
  return (
    <select
      id={props.id}
      class={selectClass}
      aria-label={t("navbar.theme")}
      value={shell.theme()}
      onChange={(e) => shell.setTheme(e.currentTarget.value as Theme)}
    >
      <option value="light">{t("navbar.themes.light")}</option>
      <option value="dark">{t("navbar.themes.dark")}</option>
      <option value="system">{t("navbar.themes.system")}</option>
    </select>
  );
}

function AdminToggle() {
  const shell = useShell();
  const { t } = useI18n("shell");
  const toggle = () => {
    if (shell.session.admin()) {
      shell.session
        .disableAdmin()
        .catch((err: unknown) =>
          shell.toasts.error(
            t("navbar.adminOffFailed", { message: err instanceof Error ? err.message : String(err) }),
          ),
        );
    } else {
      void shell.session.requestAdmin();
    }
  };
  return (
    <Show when={shell.session.canAdmin()}>
      <button
        type="button"
        class={shell.session.admin() ? "btn btn-sm btn-warning" : "btn btn-sm btn-ghost"}
        aria-pressed={shell.session.admin()}
        title={shell.session.admin() ? t("navbar.disableAdmin") : t("navbar.enableAdmin")}
        onClick={toggle}
      >
        <Show when={shell.session.admin()} fallback={<LockIcon />}>
          <UnlockIcon />
        </Show>
        {/* Below sm only the icon is shown (the text stays for screen readers): with the label the navbar is wider than a phone. */}
        <span class="shell-label">
          {shell.session.admin() ? t("navbar.adminOn") : t("navbar.enableAdmin")}
        </span>
      </button>
    </Show>
  );
}

function UserMenu() {
  const shell = useShell();
  const { t } = useI18n("shell");
  const navigate = useNavigate();
  // `for` and the select's id belong together (a unique pair per menu); the wrapping label alone would do, but the
  // lint rule only sees a select that stands in the label itself
  const languageId = createUniqueId();
  const themeId = createUniqueId();
  const logout = async () => {
    // logout() clears the local session first and then rethrows a failed request: leave either way. Only a 401
    // proves that the server session was gone already; any other failure may have left it alive, so say so.
    try {
      await shell.session.logout();
    } catch (err) {
      if (!(err instanceof ApiError && err.status === 401)) shell.toasts.error(t("navbar.logoutFailed"));
    }
    navigate("/login", { replace: true });
  };
  return (
    <details class="dropdown dropdown-end">
      <summary class="btn btn-sm btn-ghost">
        <span aria-hidden="true">
          <IconUser size={18} strokeWidth={1.75} />
        </span>
        <span class="shell-label">{shell.session.user()?.name}</span>
      </summary>
      <div class="dropdown-content z-20 mt-2 flex w-60 flex-col gap-2 rounded-box bg-base-200 p-3 shadow">
        {/* labels: the visible text names its select (which keeps its aria-label for the login page) */}
        <label for={languageId} class="flex items-center justify-between gap-2 text-sm">
          <span>{t("navbar.language")}</span>
          <LanguageSelect id={languageId} />
        </label>
        <label for={themeId} class="flex items-center justify-between gap-2 text-sm">
          <span>{t("navbar.theme")}</span>
          <ThemeSelect id={themeId} />
        </label>
        <div class="divider my-0" />
        <button type="button" class="btn btn-sm btn-ghost justify-start" onClick={() => void logout()}>
          <span aria-hidden="true">
            <IconLogout size={18} strokeWidth={1.75} />
          </span>
          {t("navbar.logout")}
        </button>
      </div>
    </details>
  );
}

export function Navbar() {
  const shell = useShell();
  const { t } = useI18n("shell");
  useDropdownDismiss(); // for the Host menu and the user menu
  return (
    <header class="navbar sticky top-0 z-10 gap-2 border-b border-base-300 bg-base-100 px-4">
      <label for="edelweiss-drawer" class="btn btn-square btn-ghost lg:hidden" aria-label={t("nav.open")}>
        <MenuIcon />
      </label>
      <div class="flex-1 truncate">
        <span class="font-semibold">{shell.session.info()?.hostname}</span>
        <span class="ml-2 text-xs text-base-content/60">edelweiss {shell.session.info()?.version}</span>
      </div>
      <AdminToggle />
      <HostMenu />
      <UserMenu />
    </header>
  );
}
