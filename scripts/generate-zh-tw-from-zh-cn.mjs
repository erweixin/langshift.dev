/**
 * One-off helper: zh-cn.mdx -> zh-tw.mdx via opencc-js (cn → tw).
 * Run: node scripts/generate-zh-tw-from-zh-cn.mjs
 */
import fs from "node:fs"
import path from "node:path"
import { Converter } from "opencc-js"

const root = path.resolve("content/docs")
const convert = Converter({ from: "cn", to: "tw" })

function processDir(relDir, filter) {
  const dir = path.join(root, relDir)
  for (const name of fs.readdirSync(dir)) {
    if (!name.endsWith(".zh-cn.mdx")) continue
    if (filter && !filter(name)) continue
    const src = path.join(dir, name)
    const destName = name.replace(/\.zh-cn\.mdx$/, ".zh-tw.mdx")
    const dest = path.join(dir, destName)
    if (fs.existsSync(dest)) {
      console.log("skip existing:", path.join(relDir, destName))
      continue
    }
    const text = fs.readFileSync(src, "utf8")
    fs.writeFileSync(dest, convert(text), "utf8")
    console.log("wrote:", path.join(relDir, destName))
  }
}

processDir("js2py", (n) => n.startsWith("module-"))
processDir("py2rust", (n) => n === "module-01-syntax-basics.zh-cn.mdx")
