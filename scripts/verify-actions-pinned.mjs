import { readdir, readFile } from "node:fs/promises";
import path from "node:path";

const workflowRoot = path.resolve(".github/workflows");
const files = (await readdir(workflowRoot))
  .filter((name) => name.endsWith(".yml") || name.endsWith(".yaml"))
  .sort();
const immutable = /^[0-9a-f]{40}$/;
const violations = [];

for (const name of files) {
  const lines = (await readFile(path.join(workflowRoot, name), "utf8")).split("\n");
  for (let index = 0; index < lines.length; index += 1) {
    const match = lines[index].match(/^\s*-?\s*uses:\s*([^\s#]+)(?:\s+#.*)?$/);
    if (!match || match[1].startsWith("./")) continue;
    const separator = match[1].lastIndexOf("@");
    const ref = separator >= 0 ? match[1].slice(separator + 1) : "";
    if (!immutable.test(ref)) {
      violations.push(`${name}:${index + 1}: ${match[1]}`);
    }
  }
}

if (violations.length > 0) {
  console.error("GitHub Actions must use immutable 40-character commit SHAs:");
  for (const violation of violations) console.error(`- ${violation}`);
  process.exit(1);
}

console.log(`Verified immutable Action references in ${files.length} workflow files.`);
