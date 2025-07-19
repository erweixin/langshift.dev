"use client"

import { Pre, RawCode } from "codehike/code"
import { callout } from "./annotations/callout"
import { useState } from "react"
import { Check, Copy } from "lucide-react"

interface CodeClientProps {
  codeblock: RawCode
  highlighted: any
}

export function CodeClient({ codeblock, highlighted }: CodeClientProps) {
  const [copied, setCopied] = useState(false)

  const handleCopy = async () => {
    try {
      await navigator.clipboard.writeText(codeblock.value)
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    } catch (err) {
      console.error('复制失败:', err)
    }
  }

  return (
    <div className="relative group">
      {/* 复制按钮 */}
      <button
        onClick={handleCopy}
        className="
          absolute top-3 right-3 
          p-2 
          bg-zinc-800/80 
          hover:bg-zinc-700/90 
          border border-zinc-600/50 
          rounded-md 
          opacity-0 
          group-hover:opacity-100 
          transition-all 
          duration-200 
          ease-in-out
          backdrop-blur-sm
          z-10
          focus:outline-none 
          focus:ring-2 
          focus:ring-blue-500/50 
          focus:ring-offset-2 
          focus:ring-offset-zinc-900
        "
        title="复制代码"
        aria-label="复制代码到剪贴板"
      >
        {copied ? (
          <Check className="w-4 h-4 text-green-400" />
        ) : (
          <Copy className="w-4 h-4 text-zinc-300" />
        )}
      </button>

      {/* 复制成功提示 */}
      {copied && (
        <div className="
          absolute top-12 right-3 
          px-3 py-1 
          bg-green-600/90 
          text-white 
          text-sm 
          rounded-md 
          opacity-0 
          animate-in 
          slide-in-from-top-2 
          duration-200
          backdrop-blur-sm
          z-20
        ">
          已复制！
        </div>
      )}

      <Pre 
        code={highlighted} 
        handlers={[callout]} 
        className="
          border border-zinc-700/50 
          bg-zinc-900/95 
          rounded-lg 
          p-4 
          my-6 
          shadow-lg 
          shadow-black/20 
          backdrop-blur-sm
          overflow-x-auto
          [&_pre]:!bg-transparent
          [&_pre]:!p-0
          [&_pre]:!m-0
          [&_code]:!bg-transparent
          [&_code]:!p-0
          [&_code]:!m-0
          [&_code]:!text-sm
          [&_code]:!leading-relaxed
          [&_.ch-codeblock]:!bg-transparent
          [&_.ch-codeblock]:!p-0
          [&_.ch-codeblock]:!m-0
          [&_.ch-codegroup]:!bg-transparent
          [&_.ch-codegroup]:!p-0
          [&_.ch-codegroup]:!m-0
          [&_.ch-code-scroll-parent]:!bg-transparent
          [&_.ch-code-scroll-parent]:!p-0
          [&_.ch-code-scroll-parent]:!m-0
          [&_.ch-code-scroll-parent]:!rounded-none
          [&_.ch-code-scroll-parent]:!border-none
          [&_.ch-code-scroll-parent]:!shadow-none
        " 
      />
    </div>
  )
} 