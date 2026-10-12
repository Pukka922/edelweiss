import { useI18n } from "@edelweiss/sdk";
import type { JSX } from "solid-js";
import { Navbar } from "./Navbar";
import { Sidebar } from "./Sidebar";

export function Layout(props: { children: JSX.Element }) {
  const { t } = useI18n("shell");
  return (
    <div class="drawer lg:drawer-open">
      <input id="edelweiss-drawer" type="checkbox" class="drawer-toggle" aria-label={t("nav.open")} />
      <div class="drawer-content flex min-h-screen flex-col">
        <Navbar />
        <main class="flex-1 shell-main">{props.children}</main>
      </div>
      <div class="drawer-side z-20">
        <label for="edelweiss-drawer" aria-label={t("nav.open")} class="drawer-overlay" />
        <Sidebar />
      </div>
    </div>
  );
}
