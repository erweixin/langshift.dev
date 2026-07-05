import { useMemo, useState } from "react";
import {
  QueryClient,
  QueryClientProvider,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { Link, Route, Routes } from "react-router";
import type { ContentGenerationInput, ContentGenerationRun, PublicContentArtifact } from "./contracts";

const queryClient = new QueryClient();

const taskSeed: ContentGenerationInput = {
  task_template_id: "fe2agent-d01",
  target_stack: "frontend_to_agent",
  level_band: "default",
  title: "API boundaries",
  judge: "Explain API acceptance versus worker execution.",
  minutes: 30,
  context: {
    source: "frontend-smoke",
  },
};

export default function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <Routes>
        <Route path="/" element={<Shell />} />
      </Routes>
    </QueryClientProvider>
  );
}

function Shell() {
  const queryClient = useQueryClient();
  const [runId, setRunId] = useState("");

  const health = useQuery({
    queryKey: ["health"],
    queryFn: getHealth,
    retry: false,
  });

  const startGeneration = useMutation({
    mutationFn: () => createContentGenerationRun(taskSeed),
    onSuccess: (result) => {
      setRunId(result.run_id);
      queryClient.setQueryData(["content-run", result.run_id], result);
    },
  });

  const run = useQuery({
    queryKey: ["content-run", runId],
    queryFn: () => getContentGenerationRun(runId),
    enabled: runId !== "",
    refetchInterval: (query) => {
      const data = query.state.data as ContentGenerationRun | undefined;
      return data && isRunPending(data.status) ? 1500 : false;
    },
  });

  const currentRun = run.data ?? startGeneration.data;
  const contentKey = currentRun?.content_key ?? "";

  const artifact = useQuery({
    queryKey: ["content-artifact", contentKey],
    queryFn: () => getContentArtifact(contentKey),
    enabled: contentKey !== "",
  });

  const progress = useMemo(() => buildProgress(currentRun?.status), [currentRun?.status]);
  const busy = startGeneration.isPending || (currentRun ? isRunPending(currentRun.status) : false);

  return (
    <main className="app-shell">
      <aside className="rail" aria-label="Primary navigation">
        <div className="brand">Lites</div>
        <nav className="nav-list">
          <Link to="/" className="active">
            内容生成
          </Link>
          <Link to="/">今日一步</Link>
          <Link to="/">能力地图</Link>
        </nav>
      </aside>

      <section className="workspace">
        <div className="status-strip">
          <span>Content Pipeline</span>
          <strong className={health.isError ? "tone-danger" : ""}>
            {health.data?.status === "ok" ? "Backend ready" : health.isError ? "Backend unavailable" : "Checking"}
          </strong>
        </div>

        <section className="control-panel">
          <div className="task-block">
            <p className="eyebrow">Task seed</p>
            <h1>{taskSeed.title}</h1>
            <p className="summary">{taskSeed.judge}</p>
            <dl className="task-meta">
              <div>
                <dt>Template</dt>
                <dd>{taskSeed.task_template_id}</dd>
              </div>
              <div>
                <dt>Stack</dt>
                <dd>{taskSeed.target_stack}</dd>
              </div>
              <div>
                <dt>Band</dt>
                <dd>{taskSeed.level_band}</dd>
              </div>
              <div>
                <dt>Minutes</dt>
                <dd>{taskSeed.minutes}</dd>
              </div>
            </dl>
          </div>

          <div className="run-panel">
            <div className="run-heading">
              <div>
                <p className="eyebrow">Run</p>
                <h2>{currentRun?.run_id ?? "No active run"}</h2>
              </div>
              <StatusPill status={currentRun?.status} cacheHit={currentRun?.cache_hit} />
            </div>

            <button className="primary-action" type="button" disabled={busy} onClick={() => startGeneration.mutate()}>
              {busy ? "生成中" : currentRun?.status === "succeeded" ? "再次生成" : "生成内容"}
            </button>

            <div className="progress-list" aria-label="Run progress">
              {progress.map((step) => (
                <div className={`progress-item ${step.state}`} key={step.label}>
                  <span />
                  <strong>{step.label}</strong>
                </div>
              ))}
            </div>

            {currentRun?.content_key ? (
              <div className="content-key">
                <span>content_key</span>
                <code>{currentRun.content_key}</code>
              </div>
            ) : null}
          </div>
        </section>

        {startGeneration.error ? <ErrorPanel title="Create failed" error={startGeneration.error} /> : null}
        {run.error ? <ErrorPanel title="Run read failed" error={run.error} /> : null}
        {currentRun?.status === "failed" ? <RunFailure run={currentRun} /> : null}
        {artifact.error ? <ErrorPanel title="Artifact read failed" error={artifact.error} /> : null}

        {artifact.data ? (
          <ArtifactView artifact={artifact.data} />
        ) : (
          <section className="empty-panel">
            <p className="eyebrow">Artifact</p>
            <h2>{currentRun ? "Waiting for content" : "Ready"}</h2>
            <p>
              {currentRun
                ? "The run has not produced a readable artifact yet."
                : "No content generation run has been created in this session."}
            </p>
          </section>
        )}
      </section>
    </main>
  );
}

function StatusPill({ status, cacheHit }: { status?: string; cacheHit?: boolean }) {
  const label = cacheHit ? "cache hit" : status ?? "idle";
  return <span className={`status-pill ${status ?? "idle"}`}>{label}</span>;
}

function RunFailure({ run }: { run: ContentGenerationRun }) {
  return (
    <section className="error-panel">
      <strong>{run.error?.code ?? "run_failed"}</strong>
      <p>{run.error?.message ?? "Content generation failed."}</p>
    </section>
  );
}

function ErrorPanel({ title, error }: { title: string; error: unknown }) {
  return (
    <section className="error-panel">
      <strong>{title}</strong>
      <p>{error instanceof Error ? error.message : "Unknown error"}</p>
    </section>
  );
}

function ArtifactView({ artifact }: { artifact: PublicContentArtifact }) {
  return (
    <section className="artifact-layout">
      <article className="lesson-surface">
        <div className="lesson-head">
          <div>
            <p className="eyebrow">Lesson</p>
            <h2>{artifact.lesson.title}</h2>
          </div>
          <div className="artifact-flags">
            <span>{artifact.review_status ?? "unknown"}</span>
            <span>{artifact.lesson.minutes} min</span>
            <span>{artifact.validation_attempts ?? 0} checks</span>
          </div>
        </div>

        {artifact.lesson.why ? <p className="why-block">{artifact.lesson.why}</p> : null}

        <div className="section-list">
          {artifact.lesson.sections.map((section, index) => (
            <section className="lesson-section" key={section.id}>
              <span className="section-index">{String(index + 1).padStart(2, "0")}</span>
              <div>
                <h3>{section.title}</h3>
                <MarkdownText text={section.body_md} />
                {section.runnable?.demo_code ? <CodeBlock code={section.runnable.demo_code} /> : null}
              </div>
            </section>
          ))}
        </div>
      </article>

      <aside className="exercise-panel">
        <p className="eyebrow">Exercise</p>
        <h2>{artifact.exercise.language}</h2>
        <CodeBlock code={artifact.exercise.starter_code} />
        <div className="test-list">
          {artifact.exercise.tests.map((test) => (
            <div className="test-row" key={test.id}>
              <div>
                <strong>{test.label}</strong>
                <code>{test.call}</code>
              </div>
              <span>{test.judge ? "judge" : "case"}</span>
            </div>
          ))}
        </div>
      </aside>
    </section>
  );
}

function MarkdownText({ text }: { text: string }) {
  return (
    <div className="markdown-text">
      {text.split(/\n{2,}/).map((paragraph, index) => (
        <p key={`${index}-${paragraph.slice(0, 24)}`}>{paragraph}</p>
      ))}
    </div>
  );
}

function CodeBlock({ code }: { code: string }) {
  return <pre className="code-block">{code}</pre>;
}

function buildProgress(status?: string) {
  const labels = ["accepted", "queued", "executing", "succeeded"];
  const normalized = status === "started" ? "executing" : status;
  const terminalFailed = normalized === "failed";
  const currentIndex = labels.indexOf(normalized ?? "");

  return labels.map((label, index) => ({
    label,
    state:
      terminalFailed && index === labels.length - 1
        ? "failed"
        : currentIndex > index
          ? "done"
          : currentIndex === index
            ? "active"
            : "idle",
  }));
}

function isRunPending(status?: string) {
  return status === "accepted" || status === "queued" || status === "executing";
}

async function getHealth() {
  return requestJSON<{ status: string; service: string }>("/health");
}

async function createContentGenerationRun(input: ContentGenerationInput) {
  return requestJSON<ContentGenerationRun>("/api/content-generation-runs", {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      "Idempotency-Key": newIdempotencyKey(),
      "X-User-ID": "local-user",
    },
    body: JSON.stringify(input),
  });
}

async function getContentGenerationRun(runId: string) {
  return requestJSON<ContentGenerationRun>(`/api/content-generation-runs/${encodeURIComponent(runId)}`, {
    headers: { "X-User-ID": "local-user" },
  });
}

async function getContentArtifact(contentKey: string) {
  return requestJSON<PublicContentArtifact>(`/api/content-artifacts/${encodeURIComponent(contentKey)}`, {
    headers: { "X-User-ID": "local-user" },
  });
}

async function requestJSON<T>(input: RequestInfo | URL, init?: RequestInit): Promise<T> {
  const response = await fetch(input, init);
  const text = await response.text();
  const payload = text ? JSON.parse(text) : undefined;
  if (!response.ok) {
    const message = typeof payload?.error === "string" ? payload.error : `Request failed: ${response.status}`;
    throw new Error(message);
  }
  return payload as T;
}

function newIdempotencyKey() {
  if (typeof crypto !== "undefined" && "randomUUID" in crypto) {
    return `content-${crypto.randomUUID()}`;
  }
  return `content-${Date.now()}-${Math.random().toString(36).slice(2)}`;
}
