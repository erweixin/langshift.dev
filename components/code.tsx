import { Pre, RawCode, highlight } from "codehike/code"
import { callout } from "./annotations/callout"

export async function Code({ codeblock }: { codeblock: RawCode }) {
  const highlighted = await highlight(codeblock, "github-dark")
  return (
    <Pre code={highlighted} handlers={[callout]} className="border bg-card bg-zinc-900" />
  )
}
