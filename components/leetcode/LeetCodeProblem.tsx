import { Badge } from "@/components/ui/badge"

type LeetCodeDifficulty = "Easy" | "Medium" | "Hard"

type LeetCodeTag = {
  slug: string
  name: string
  translatedName: string | null
}

type LeetCodeQuestion = {
  questionFrontendId: string
  title: string
  translatedTitle: string | null
  difficulty: LeetCodeDifficulty
  content: string | null
  translatedContent: string | null
  topicTags: LeetCodeTag[]
}

function difficultyVariant(difficulty: LeetCodeDifficulty) {
  switch (difficulty) {
    case "Easy":
      return "beginner" as const
    case "Medium":
      return "intermediate" as const
    case "Hard":
      return "advanced" as const
    default:
      return "outline" as const
  }
}

async function fetchLeetCodeQuestion(titleSlug: string): Promise<LeetCodeQuestion> {
  const query = `
    query($titleSlug: String!) {
      question(titleSlug: $titleSlug) {
        questionFrontendId
        title
        translatedTitle
        difficulty
        content
        translatedContent
        topicTags { slug name translatedName }
      }
    }
  `

  const res = await fetch("https://leetcode.cn/graphql", {
    method: "POST",
    headers: {
      "content-type": "application/json",
      "user-agent": "Mozilla/5.0",
      referer: `https://leetcode.cn/problems/${titleSlug}/`,
    },
    body: JSON.stringify({ query, variables: { titleSlug } }),
    next: { revalidate: 60 * 60 * 24 * 7 }, // 7 days
  })

  if (!res.ok) {
    throw new Error(`Failed to fetch LeetCode question: ${res.status} ${res.statusText}`)
  }

  const json = (await res.json()) as {
    data?: { question?: LeetCodeQuestion | null }
    errors?: Array<{ message: string }>
  }

  if (json.errors?.length) {
    throw new Error(json.errors.map((e) => e.message).join("; "))
  }

  const question = json.data?.question
  if (!question) {
    throw new Error("Question not found")
  }

  return question
}

export async function LeetCodeProblem({ titleSlug }: { titleSlug: string }) {
  const leetcodeCnUrl = `https://leetcode.cn/problems/${titleSlug}/`

  try {
    const q = await fetchLeetCodeQuestion(titleSlug)
    const title = q.translatedTitle || q.title
    const html = q.translatedContent || q.content || ""

    return (
      <section className="my-4 rounded-lg border p-4">
        <div className="flex flex-wrap items-center gap-2">
          <a className="text-sm underline underline-offset-4" href={leetcodeCnUrl} target="_blank" rel="noreferrer">
            力扣原题：{q.questionFrontendId}. {title}
          </a>
          <Badge variant={difficultyVariant(q.difficulty)}>{q.difficulty}</Badge>
          {q.topicTags?.slice(0, 6)?.map((t) => (
            <Badge key={t.slug} variant="outline">
              {t.translatedName || t.name}
            </Badge>
          ))}
        </div>

        <div className="prose prose-neutral mt-3 max-w-none dark:prose-invert" dangerouslySetInnerHTML={{ __html: html }} />
      </section>
    )
  } catch (err) {
    return (
      <section className="my-4 rounded-lg border p-4">
        <p className="text-sm">
          原题内容拉取失败（可能是网络或站点限制）。你仍可直接访问：
          <a className="ml-1 underline underline-offset-4" href={leetcodeCnUrl} target="_blank" rel="noreferrer">
            {leetcodeCnUrl}
          </a>
        </p>
        <pre className="mt-2 whitespace-pre-wrap text-xs opacity-80">{String(err)}</pre>
      </section>
    )
  }
}
