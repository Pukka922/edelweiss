import tailwindcss from "@tailwindcss/vite";
import { defineConfig } from "vite";
import solid from "vite-plugin-solid";
import { sharedSpecifiers, vendor } from "./plugins/vendor.ts";

const escapeRegExp = (s: string) => s.replace(/[.*+?^${}()|[\]\\/]/g, "\\$&");

export default defineConfig({
  plugins: [vendor(), solid(), tailwindcss()],
  build: {
    outDir: "dist",
    emptyOutDir: true,
    rolldownOptions: { external: sharedSpecifiers.map((s) => new RegExp(`^${escapeRegExp(s)}$`)) },
  },
});
