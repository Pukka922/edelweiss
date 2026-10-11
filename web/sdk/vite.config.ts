import { defineConfig } from "vite";
import solid from "vite-plugin-solid";

export default defineConfig({
  plugins: [solid()],
  build: {
    lib: { entry: "src/index.ts", formats: ["es"], fileName: () => "index.js" },
    outDir: "dist",
    emptyOutDir: true,
    rolldownOptions: { external: [/^solid-js(\/.*)?$/, /^@solidjs\/router$/] },
  },
});
