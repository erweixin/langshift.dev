import { Pre, RawCode, highlight } from "codehike/code"
import { callout } from "./annotations/callout"

export async function SideBySideCode(props: { code: RawCode[] }) {
  
  const [leftBlock, rightBlock] = props.code
  
  // 高亮两个代码块
  const [leftHighlighted, rightHighlighted] = await Promise.all([
    highlight(leftBlock, "github-dark"),
    highlight(rightBlock, "github-dark"),
  ])

  return (
    <div className="grid grid-cols-1 lg:grid-cols-2 gap-4 my-6">
      <div className="relative">
        {leftBlock.meta && (
          <div className="absolute -top-3 left-4 z-10 bg-background px-2 text-sm font-medium text-muted-foreground">
            {leftBlock.meta}
          </div>
        )}
        <Pre 
          code={leftHighlighted} 
          handlers={[callout]} 
          className="border bg-card h-full bg-zinc-900" 
        />
      </div>
      <div className="relative">
        {rightBlock.meta && (
          <div className="absolute -top-3 left-4 z-10 bg-background px-2 text-sm font-medium text-muted-foreground">
            {rightBlock.meta}
          </div>
        )}
        <Pre 
          code={rightHighlighted} 
          handlers={[callout]} 
          className="border bg-card h-full bg-zinc-900" 
        />
      </div>
    </div>
  )
} 