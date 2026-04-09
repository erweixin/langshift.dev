/**
 * Content quality checks for content/docs MDX (lightweight frontmatter + fenced code).
 * Run: pnpm content-check
 */
import fs from "node:fs"
import path from "node:path"

const root = path.resolve("content/docs")
let errors = 0

function walk(dir, out = []) {
  for (const name of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, name.name)
    if (name.isDirectory()) walk(p, out)
    else if (name.name.endsWith(".mdx")) out.push(p)
  }
  return out
}

function parseFrontmatter(source) {
  if (!source.startsWith("---")) return null
  const nl = source.indexOf("\n", 0)
  const end = source.indexOf("\n---", 3)
  if (end === -1) return null
  return source.slice(nl + 1, end)
}

const files = walk(root)
for (const file of files) {
  const rel = path.relative(root, file)
  const src = fs.readFileSync(file, "utf8")
  const block = parseFrontmatter(src)
  if (!block) {
    console.error(`[ERR] ${rel}: missing or invalid YAML frontmatter (opening --- ... ---)`)
    errors++
    continue
  }
  if (!/^\s*title\s*:/m.test(block)) {
    console.error(`[ERR] ${rel}: frontmatter must include title`)
    errors++
  }
  if (src.includes("```") && !/\bUniversalEditor\b/.test(src) && !/\bPythonEditor\b/.test(src) && !/<Code\b/.test(src)) {
    console.error(`[ERR] ${rel}: fenced code (\`\`\`) requires UniversalEditor, PythonEditor, or <Code> wrapper`)
    errors++
  }
}

if (errors) {
  console.error(`\ncontent-check failed: ${errors} issue(s)`)
  process.exit(1)
}
console.log(`content-check OK (${files.length} MDX files)`)
