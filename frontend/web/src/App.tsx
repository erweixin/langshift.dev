import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
import { Link, Route, Routes } from "react-router";

const queryClient = new QueryClient();

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
  const health = useQuery({
    queryKey: ["health"],
    queryFn: async () => {
      const response = await fetch("/health");
      if (!response.ok) {
        throw new Error(`Health check failed: ${response.status}`);
      }
      return (await response.json()) as { status: string; service: string };
    },
    retry: false,
  });

  return (
    <main className="app-shell">
      <aside className="rail" aria-label="Primary navigation">
        <div className="brand">Lites</div>
        <nav className="nav-list">
          <Link to="/">今日一步</Link>
          <Link to="/">成长证据</Link>
          <Link to="/">能力地图</Link>
        </nav>
      </aside>

      <section className="workspace">
        <div className="status-strip">
          <span>Phase 0</span>
          <strong>{health.data?.status === "ok" ? "Backend ready" : "Initializing"}</strong>
        </div>

        <div className="focus-panel">
          <p className="eyebrow">工程初始化</p>
          <h1>前后端骨架已准备接入 Daily Loop</h1>
          <p className="summary">
            React TypeScript、TanStack Query、Router、共享 contracts 和 Go API 会在这里汇合。下一阶段开始接入真实事件流和 Review worker。
          </p>
        </div>
      </section>
    </main>
  );
}
