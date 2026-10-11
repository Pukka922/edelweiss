import { spawn } from "node:child_process";

const workspaces = ["@edelweiss/sdk", "@edelweiss/shell"];
const children = workspaces.map((ws) =>
  spawn("npm", ["run", "watch", "-w", ws], { stdio: "inherit", shell: false }),
);
const stop = () => {
  for (const child of children) child.kill("SIGTERM");
};
process.on("SIGINT", stop);
process.on("SIGTERM", stop);
for (const child of children) {
  child.on("exit", (code) => {
    if (code) {
      stop();
      process.exitCode = code;
    }
  });
}
