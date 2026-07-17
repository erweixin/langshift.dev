import type { paths } from "./schema";

export type MissionsResponse = paths["/v1/missions"]["get"]["responses"][200]["content"]["application/json"];
export type DailyTasksResponse = paths["/v1/daily-tasks"]["get"]["responses"][200]["content"]["application/json"];
export type PreferencesResponse = paths["/v1/preferences"]["get"]["responses"][200]["content"]["application/json"];

export class ApiError extends Error {
  constructor(
    message: string,
    readonly status: number,
    readonly requestID: string | null,
  ) {
    super(message);
    this.name = "ApiError";
  }
}

type RequestOptions = {
  method?: "GET" | "POST" | "PUT" | "PATCH" | "DELETE";
  body?: unknown;
  signal?: AbortSignal;
  idempotencyKey?: string;
  csrfToken?: string;
  ifMatch?: string;
  accept?: string;
  contentType?: string;
};

export async function apiRequest<T>(path: string, options: RequestOptions = {}): Promise<T> {
  if (!path.startsWith("/v1/")) throw new Error("API path must be versioned");
  const method = options.method ?? "GET";
  if (typeof navigator !== "undefined" && !navigator.onLine && method !== "GET") {
    throw new ApiError("offline_write_blocked", 0, null);
  }
  const headers = new Headers({ Accept: options.accept ?? "application/json" });
  if (options.body !== undefined) headers.set("Content-Type", options.contentType ?? "application/json");
  if (options.idempotencyKey) headers.set("Idempotency-Key", options.idempotencyKey);
  if (options.csrfToken) headers.set("X-CSRF-Token", options.csrfToken);
  if (options.ifMatch) headers.set("If-Match", options.ifMatch);
  const response = await fetch(`/api${path}`, {
    method,
    headers,
    body: options.body === undefined ? undefined : JSON.stringify(options.body),
    credentials: "include",
    cache: "no-store",
    signal: options.signal,
  });
  if (!response.ok) {
    let message = `request_failed_${response.status}`;
    try {
      const problem = (await response.json()) as { code?: string; title?: string };
      message = problem.code ?? problem.title ?? message;
    } catch {
      // The status and request ID still give support a safe diagnostic handle.
    }
    throw new ApiError(message, response.status, response.headers.get("X-Request-ID"));
  }
  if (response.status === 204) return undefined as T;
  return (await response.json()) as T;
}

export function newIdempotencyKey(): string {
  return crypto.randomUUID();
}
