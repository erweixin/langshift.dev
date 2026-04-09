import type { ReactNode } from "react"
import { RawCode, highlight } from "codehike/code"
import { CodeClient } from "./code-client"

interface CodeProps {
  /** Code Hike（MDX 代码块）传入 */
  codeblock?: RawCode
  /** 手写 <Code language="…">…</Code> 时的语言 */
  language?: string
  children?: ReactNode
}

function childrenToString(children: ReactNode): string {
  if (children == null) return ""
  if (typeof children === "string") return children
  if (typeof children === "number") return String(children)
  if (Array.isArray(children)) return children.map(childrenToString).join("")
  return ""
}

function resolveCodeblock({
  codeblock,
  language,
  children,
}: CodeProps): RawCode {
  if (codeblock) return codeblock
  const lang = (language ?? "").trim() || "txt"
  return {
    value: childrenToString(children),
    lang,
    meta: "",
  }
}

// 服务器端组件 - 处理代码高亮
export async function Code(props: CodeProps) {
  const codeblock = resolveCodeblock(props)
  const highlighted = await highlight(codeblock, "github-dark")
  return <CodeClient codeblock={codeblock} highlighted={highlighted} />
}
