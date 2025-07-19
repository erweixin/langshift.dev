import { Pre, RawCode, highlight } from "codehike/code"
import { callout } from "./annotations/callout"
import { CodeClient } from "./code-client"

interface CodeProps {
  codeblock: RawCode
}

// 服务器端组件 - 处理代码高亮
export async function Code({ codeblock }: CodeProps) {
  const highlighted = await highlight(codeblock, "github-dark")
  return <CodeClient codeblock={codeblock} highlighted={highlighted} />
}
