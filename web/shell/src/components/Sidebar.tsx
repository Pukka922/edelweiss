import { localized, useI18n } from "@edelweiss/sdk";
import { A } from "@solidjs/router";
import { createMemo, For } from "solid-js";
import { useShell } from "../context";
import { groupBySection } from "../state/extensions";
import { PuzzleIcon } from "./Icons";

function ExtensionIcon(props: { icon?: string }) {
  const mask = () =>
    props.icon
      ? {
          "mask-image": `url("${props.icon}")`,
          "-webkit-mask-image": `url("${props.icon}")`,
          "mask-size": "contain",
          "mask-repeat": "no-repeat",
          "mask-position": "center",
        }
      : undefined;
  return <span class="inline-block size-5 shrink-0 bg-current" style={mask()} aria-hidden="true" />;
}

export function Sidebar() {
  const shell = useShell();
  const { t } = useI18n("shell");
  const groups = createMemo(() => groupBySection(shell.extensions.list(), shell.i18n.locale()));
  return (
    <aside class="flex min-h-full w-64 flex-col gap-2 bg-base-200 p-4">
      <A href="/" end class="mb-2 flex items-center gap-2 px-2 text-lg font-bold">
        <img src="/favicon.svg" alt="" class="size-7" />
        {t("app.name")}
      </A>
      <ul class="menu w-full gap-1 p-0">
        <For each={groups()}>
          {(group) => (
            <li>
              <h2 class="menu-title">{t(`nav.sections.${group.section}`)}</h2>
              <ul>
                <For each={group.items}>
                  {(ext) => (
                    <li>
                      <A href={`/x/${ext.id}`} activeClass="menu-active">
                        <ExtensionIcon icon={ext.icon} />
                        {localized(ext.name, shell.i18n.locale())}
                      </A>
                    </li>
                  )}
                </For>
              </ul>
            </li>
          )}
        </For>
        <li>
          <h2 class="menu-title">{t("nav.settings")}</h2>
          <ul>
            <li>
              <A href="/settings/extensions" activeClass="menu-active">
                <PuzzleIcon />
                {t("nav.extensions")}
              </A>
            </li>
          </ul>
        </li>
      </ul>
    </aside>
  );
}
