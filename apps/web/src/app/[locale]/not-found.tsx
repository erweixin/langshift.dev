import Link from "next/link";

export default function NotFound() {
  return <main className="centered-state" id="main-content"><p className="eyebrow">404</p><h1>This path is not on your route.</h1><p>The page may have moved, but your Mission and evidence are unchanged.</p><Link className="button primary" href="/en/today">Return to Today</Link></main>;
}
