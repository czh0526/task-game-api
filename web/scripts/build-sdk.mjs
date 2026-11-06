import { spawnSync } from "node:child_process";
import { cpSync, existsSync, lstatSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const webRoot = join(dirname(fileURLToPath(import.meta.url)), "..");
const sdkRoot = join(webRoot, "../sdk/ts");
const tsc = join(sdkRoot, "node_modules/typescript/bin/tsc");

if (!existsSync(tsc)) {
  const install = spawnSync("npm", ["ci"], { cwd: sdkRoot, stdio: "inherit" });
  if (install.status !== 0) process.exit(install.status ?? 1);
}

const build = spawnSync("npm", ["run", "build"], { cwd: sdkRoot, stdio: "inherit" });
if (build.status !== 0) process.exit(build.status ?? 1);

// `file:` installs are usually symlinks. A packed copy would not see dist
// written into the source tree, so copy the build output into that copy.
const installed = join(webRoot, "node_modules/taskgame-client");
if (existsSync(installed) && !lstatSync(installed).isSymbolicLink()) {
  cpSync(join(sdkRoot, "dist"), join(installed, "dist"), { recursive: true });
}
