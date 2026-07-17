"use client";

export default function ErrorPage({ reset }: { error: Error & { digest?: string }; reset: () => void }) {
  return <main className="centered-state" id="main-content"><p className="eyebrow">Workspace unavailable</p><h1>We couldn’t load this view.</h1><p>Your work has not been marked complete. Check the connection and try again.</p><button className="button primary" type="button" onClick={reset}>Try again</button></main>;
}
