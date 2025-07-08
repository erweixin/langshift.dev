import { remarkCodeHike, recmaCodeHike, CodeHikeConfig } from "codehike/mdx"
import { defineDocs, defineConfig } from "fumadocs-mdx/config"

export const docs = defineDocs({
  dir: 'content/docs',
});

const chConfig: CodeHikeConfig = {
  components: {
    code: "Code",
  },
}

export default defineConfig({
  mdxOptions: {
    remarkPlugins: (v) => [[remarkCodeHike, chConfig], ...v],
    recmaPlugins: [[recmaCodeHike, chConfig]],
  },
})
