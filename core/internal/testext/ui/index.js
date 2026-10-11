// The page of the test extension in the shell, in plain JavaScript without a build step: "@edelweiss/sdk" and
// "solid-js" are bare imports, which the shell's import map resolves as for a built extension UI. The core's Go tests
// serve this file, and the dev environment shows it after installing testext from the mock GitHub (make mock-releases).
// web/shell/src/pages/testext.test.tsx loads it with the shell's ExtensionHost.
import { defineExtension, PageHeader, useApi } from "@edelweiss/sdk";
import { createComponent } from "solid-js";

function TestExtension() {
  const version = document.createElement("p");
  version.textContent = "…";
  useApi()
    .get("/version")
    .then(
      (answer) => {
        version.textContent = `Version ${answer.version}`;
      },
      (err) => {
        version.textContent = err instanceof Error ? err.message : String(err);
      },
    );
  return [
    createComponent(PageHeader, { title: "Test extension", subtitle: "core/internal/testext" }),
    version,
  ];
}

export default defineExtension({ component: TestExtension });
