export function GET() {
  return Response.json({ status: "live" }, { headers: { "Cache-Control": "no-store" } });
}
