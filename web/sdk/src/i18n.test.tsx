import { render } from "@solidjs/testing-library";
import { describe, expect, it } from "vitest";
import { ExtensionContext, extensionContextFor } from "./extension";
import { createI18n, I18nContext, useI18n } from "./i18n";

describe("createI18n", () => {
  it("translates nested keys with parameters and falls back to en, then to the key", () => {
    const i18n = createI18n("de");
    i18n.register("shell", "en", { login: { hello: "Hello {{ name }}", only: "English only" } });
    i18n.register("shell", "de", { login: { hello: "Hallo {{ name }}" } });
    expect(i18n.translate("shell", "login.hello", { name: "myUser" })).toBe("Hallo myUser");
    expect(i18n.translate("shell", "login.only")).toBe("English only");
    expect(i18n.translate("shell", "login.missing")).toBe("login.missing");
    i18n.setLocale("en");
    expect(i18n.translate("shell", "login.hello", { name: "myUser" })).toBe("Hello myUser");
  });

  it("ships the sdk namespace", () => {
    const i18n = createI18n("de");
    expect(i18n.translate("sdk", "confirm.cancel")).toBe("Abbrechen");
  });

  it("useI18n scopes to the current extension and reacts to locale changes", () => {
    const i18n = createI18n("en");
    i18n.register("system", "en", { title: "Overview" });
    i18n.register("system", "de", { title: "Übersicht" });
    function Title() {
      const { t } = useI18n();
      return <h1>{t("title")}</h1>;
    }
    const { getByRole } = render(() => (
      <I18nContext.Provider value={i18n}>
        <ExtensionContext.Provider value={extensionContextFor("system")}>
          <Title />
        </ExtensionContext.Provider>
      </I18nContext.Provider>
    ));
    expect(getByRole("heading").textContent).toBe("Overview");
    i18n.setLocale("de");
    expect(getByRole("heading").textContent).toBe("Übersicht");
  });
});
