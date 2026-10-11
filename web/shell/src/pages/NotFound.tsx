import { useI18n } from "@edelweiss/sdk";
import { A } from "@solidjs/router";

export function NotFound() {
  const { t } = useI18n("shell");
  return (
    <div class="flex flex-col gap-2 p-6">
      <h1 class="text-xl font-semibold">{t("notFound.title")}</h1>
      <A href="/" class="link">
        {t("notFound.back")}
      </A>
    </div>
  );
}
