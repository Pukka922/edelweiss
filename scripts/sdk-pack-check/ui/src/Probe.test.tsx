import { createI18n } from "@edelweiss/sdk";
import { TestShell } from "@edelweiss/sdk/testing";
import { cleanup, fireEvent, render } from "@solidjs/testing-library";
import { afterEach, expect, it } from "vitest";
import { Probe } from "./Probe";

afterEach(cleanup);

// TestShell (testing.js) and useI18n (index.js) must share the SDK contexts, and the component one solid-js.
it("renders inside TestShell and reacts to clicks", () => {
  const i18n = createI18n("en");
  i18n.register("probe", "en", { title: "Probe title" });
  const { getByText, getByRole } = render(() => (
    <TestShell extension="probe" i18n={i18n}>
      <Probe />
    </TestShell>
  ));
  expect(getByText("Probe title")).toBeTruthy();
  fireEvent.click(getByRole("button"));
  expect(getByRole("button").textContent).toBe("1");
});
