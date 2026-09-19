// Deletes the build in web/dist and restores the versioned placeholder
// index.html, leaving the tree as in a clean clone.
import { readdirSync, rmSync } from "node:fs";
import { execFileSync } from "node:child_process";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const dist = join(dirname(fileURLToPath(import.meta.url)), "..", "dist");

for (const name of readdirSync(dist)) {
  if (name !== "index.html") rmSync(join(dist, name), { recursive: true, force: true });
}
execFileSync("git", ["checkout", "--", "index.html"], { cwd: dist, stdio: "inherit" });
console.log("web/dist restored to the versioned placeholder");
