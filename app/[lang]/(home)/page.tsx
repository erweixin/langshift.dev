import { redirect } from "next/navigation"

const Redirect = async ({ params }: { params: Promise<{ lang: string }> }) => {
  const lang = (await params).lang;

  return <div>home</div>
}

export default Redirect