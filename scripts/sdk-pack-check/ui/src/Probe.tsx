import { PageHeader, useI18n } from "@edelweiss/sdk";
import { createSignal } from "solid-js";

export function Probe() {
  const { t } = useI18n();
  const [count, setCount] = createSignal(0);
  return (
    <div>
      <PageHeader title={t("title")} />
      <button type="button" class="badge badge-primary" onClick={() => setCount(count() + 1)}>
        {count()}
      </button>
    </div>
  );
}
