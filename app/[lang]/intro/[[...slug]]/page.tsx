import Link from "next/link"

export default function HomePage() {
  return (
    <main className="min-h-screen">
      {/* Hero 区域 */}
      <section className="relative overflow-hidden bg-gradient-to-br from-slate-900 via-purple-900 to-slate-900 py-24">
        {/* 背景装饰 */}
        <div className="absolute inset-0">
          <div className="absolute inset-0 bg-[radial-gradient(circle_at_50%_50%,rgba(120,119,198,0.1),transparent_50%)]"></div>
          <div className="absolute top-0 left-0 w-72 h-72 bg-purple-500 rounded-full mix-blend-multiply filter blur-xl opacity-20 animate-blob"></div>
          <div className="absolute top-0 right-0 w-72 h-72 bg-blue-500 rounded-full mix-blend-multiply filter blur-xl opacity-20 animate-blob animation-delay-2000"></div>
          <div className="absolute -bottom-8 left-20 w-72 h-72 bg-pink-500 rounded-full mix-blend-multiply filter blur-xl opacity-20 animate-blob animation-delay-4000"></div>
        </div>
        
        <div className="container mx-auto px-4 text-center relative z-10">
          <div className="mb-8">
            <span className="inline-flex items-center px-6 py-3 rounded-full bg-white/10 backdrop-blur-sm text-white text-sm font-medium mb-6 border border-white/20">
              <span className="mr-2">🚀</span>
              专为前端工程师设计
            </span>
          </div>
          <h1 className="text-6xl md:text-8xl font-bold text-white mb-8 leading-tight">
            <span className="bg-gradient-to-r from-blue-400 via-purple-400 to-pink-400 bg-clip-text text-transparent">
              Python 教程
            </span>
            <br />
            <span className="text-white/90">for JavaScript 开发者</span>
          </h1>
          <p className="text-xl md:text-2xl text-white/80 mb-10 max-w-4xl mx-auto leading-relaxed font-light">
            从 JavaScript 开发者视角快速掌握 Python
          </p>
          <div className="flex flex-col sm:flex-row gap-6 justify-center">
            <Link
              href="/docs/module-0-python-introduction"
              className="group inline-flex items-center px-10 py-4 bg-gradient-to-r from-blue-600 to-purple-600 text-white font-semibold rounded-2xl hover:from-blue-700 hover:to-purple-700 transition-all duration-300 transform hover:scale-105 shadow-2xl hover:shadow-blue-500/25"
            >
              开始学习
              <span className="ml-2 group-hover:translate-x-1 transition-transform duration-300">→</span>
            </Link>
            <Link
              href="#modules"
              className="group inline-flex items-center px-10 py-4 bg-white/10 backdrop-blur-sm text-white font-semibold rounded-2xl hover:bg-white/20 transition-all duration-300 border border-white/20 hover:border-white/40"
            >
              查看课程大纲
              <span className="ml-2 group-hover:translate-x-1 transition-transform duration-300">↓</span>
            </Link>
          </div>
        </div>
      </section>

      {/* 特色亮点区域 */}
      <section className="py-24 bg-gradient-to-b from-slate-50 to-white dark:from-slate-900 dark:to-slate-800">
        <div className="container mx-auto px-4">
          <div className="text-center mb-20">
            <h2 className="text-4xl md:text-5xl font-bold text-slate-900 dark:text-white mb-6">
              为什么选择这个教程？
            </h2>
            <p className="text-xl text-slate-600 dark:text-slate-300 max-w-3xl mx-auto leading-relaxed">
              专为 JavaScript 开发者设计的学习路径，让你快速掌握 Python 核心技能
            </p>
          </div>
          <div className="grid md:grid-cols-3 gap-8">
            {[
              {
                icon: "🎯",
                title: "对比教学",
                desc: "从 JavaScript 开发者视角解释 Python 概念，提供 JS vs Python 的语法对比，让你快速理解核心差异",
                gradient: "from-blue-500 to-cyan-500"
              },
              {
                icon: "🎮",
                title: "交互式练习",
                desc: "每个模块都包含在线编程挑战，直接在浏览器中编码并获得即时反馈，巩固所学知识。",
                gradient: "from-pink-500 to-rose-500"
              },
              {
                icon: "💻",
                title: "实战驱动",
                desc: "通过真实项目驱动学习，包括 Web 开发、数据处理、自动化脚本等实用技能",
                gradient: "from-purple-500 to-pink-500"
              }
            ].map((feature, index) => (
              <div key={index} className="group relative">
                <div className="absolute inset-0 bg-gradient-to-r opacity-0 group-hover:opacity-100 transition-opacity duration-300 rounded-3xl blur-xl"></div>
                <div className="relative bg-white dark:bg-slate-800 rounded-3xl p-8 shadow-xl hover:shadow-2xl transition-all duration-500 transform hover:-translate-y-2 border border-slate-200 dark:border-slate-700">
                  <div className={`w-16 h-16 bg-gradient-to-r ${feature.gradient} rounded-2xl flex items-center justify-center mb-6 shadow-lg`}>
                    <span className="text-2xl">{feature.icon}</span>
                  </div>
                  <h3 className="text-2xl font-bold text-slate-900 dark:text-white mb-4">
                    {feature.title}
                  </h3>
                  <p className="text-slate-600 dark:text-slate-300 leading-relaxed">
                    {feature.desc}
                  </p>
                </div>
              </div>
            ))}
          </div>
        </div>
      </section>

      {/* 课程模块预览 */}
      <section className="py-24 bg-white dark:bg-slate-900" id="modules">
        <div className="container mx-auto px-4">
          <div className="text-center mb-20">
            <h2 className="text-4xl md:text-5xl font-bold text-slate-900 dark:text-white mb-6">
              课程模块
            </h2>
            <p className="text-xl text-slate-600 dark:text-slate-300 max-w-3xl mx-auto leading-relaxed">
              12 个精心设计的模块，从入门到进阶，全面掌握 Python
            </p>
          </div>
          <div className="grid md:grid-cols-2 lg:grid-cols-3 gap-8">
            {[
              { id: 0, link: "module-0-python-introduction", title: "入门引导", desc: "环境搭建与 pip、venv、pyenv 等核心工具", gradient: "from-blue-500 to-cyan-500" },
              { id: 1, link: "module-1-syntax-comparison", title: "语法映射", desc: "JS vs Python 语法对比", gradient: "from-green-500 to-emerald-500" },
              { id: 2, link: "module-2-module-system", title: "模块系统", desc: "Python 模块化与项目组织", gradient: "from-purple-500 to-violet-500" },
              { id: 3, link: "module-3-oop-functional", title: "面向对象", desc: "类、继承与函数式编程", gradient: "from-orange-500 to-red-500" },
              { id: 4, link: "module-4-async-programming", title: "异步编程", desc: "async/await 与事件循环", gradient: "from-red-500 to-pink-500" },
              { id: 5, link: "module-5-quality-testing-typing", title: "调试测试", desc: "单元测试与类型注解", gradient: "from-indigo-500 to-purple-500" },
              { id: 6, link: "module-6-web-development", title: "Web 开发", desc: "FastAPI 构建后端接口", gradient: "from-pink-500 to-rose-500" },
              { id: 7, link: "module-7-data-automation", title: "数据处理", desc: "pandas 与自动化脚本", gradient: "from-teal-500 to-cyan-500" },
              { id: 8, link: "module-8-projects", title: "实战项目", desc: "综合项目练习", gradient: "from-yellow-500 to-orange-500" },
              { id: 9, link: "module-9-advanced-topics", title: "进阶方向", desc: "AI、自动化与学习资源", gradient: "from-slate-500 to-gray-500" },
              { id: 10, link: "module-10-common-pitfalls", title: "常见陷阱", desc: "JS vs Python 常见概念陷阱", gradient: "from-rose-500 to-fuchsia-500" },
              { id: 11, link: "module-11-pythonic-code", title: "Pythonic 代码", desc: "学习编写地道的 Python 代码", gradient: "from-sky-500 to-indigo-500" }
            ].map((module) => (
              <Link
                key={module.id}
                href={`/docs/${module.link}`}
                className="group relative block"
              >
                <div className="absolute inset-0 bg-gradient-to-r opacity-0 group-hover:opacity-100 transition-opacity duration-300 rounded-2xl blur-xl"></div>
                <div className="relative bg-slate-50 dark:bg-slate-800 rounded-2xl p-8 hover:bg-white dark:hover:bg-slate-700 transition-all duration-300 transform hover:scale-105 border border-slate-200 dark:border-slate-700 shadow-lg hover:shadow-xl">
                  <div className="flex items-center justify-between mb-6">
                    <span className={`inline-flex items-center px-4 py-2 rounded-full text-sm font-semibold bg-gradient-to-r ${module.gradient} text-white shadow-lg`}>
                      模块 {module.id}
                    </span>
                    <span className="text-slate-400 group-hover:text-slate-600 dark:group-hover:text-slate-300 transition-colors duration-300 group-hover:translate-x-1">→</span>
                  </div>
                  <h3 className="text-xl font-bold text-slate-900 dark:text-white mb-3">
                    {module.title}
                  </h3>
                  <p className="text-slate-600 dark:text-slate-300 leading-relaxed">
                    {module.desc}
                  </p>
                </div>
              </Link>
            ))}
          </div>
        </div>
      </section>

      {/* 技术栈展示 */}
      <section className="py-24 bg-gradient-to-b from-slate-50 to-white dark:from-slate-900 dark:to-slate-800">
        <div className="container mx-auto px-4">
          <div className="text-center mb-20">
            <h2 className="text-4xl md:text-5xl font-bold text-slate-900 dark:text-white mb-6">
              技术栈
            </h2>
            <p className="text-xl text-slate-600 dark:text-slate-300 max-w-3xl mx-auto leading-relaxed">
              涵盖 Python 生态系统的核心技术和工具
            </p>
          </div>
          <div className="grid grid-cols-2 md:grid-cols-4 lg:grid-cols-6 gap-8">
            {[
              { name: "Python 3.8+", icon: "🐍", color: "blue" },
              { name: "FastAPI", icon: "⚡", color: "green" },
              { name: "pandas", icon: "📊", color: "purple" },
              { name: "asyncio", icon: "🔄", color: "orange" },
              { name: "pytest", icon: "🧪", color: "red" },
              { name: "mypy", icon: "🔍", color: "indigo" }
            ].map((tech) => (
              <div key={tech.name} className="group text-center">
                <div className="w-20 h-20 mx-auto mb-4 bg-gradient-to-br from-slate-100 to-slate-200 dark:from-slate-700 dark:to-slate-600 rounded-2xl flex items-center justify-center shadow-lg group-hover:shadow-xl transition-all duration-300 transform group-hover:scale-110">
                  <div className="text-4xl">{tech.icon}</div>
                </div>
                <div className="text-sm font-semibold text-slate-900 dark:text-white">
                  {tech.name}
                </div>
              </div>
            ))}
          </div>
        </div>
      </section>

      {/* 快速开始 */}
      <section className="py-24 bg-white dark:bg-slate-900">
        <div className="container mx-auto px-4">
          <div className="max-w-5xl mx-auto">
            <div className="text-center mb-16">
              <h2 className="text-4xl md:text-5xl font-bold text-slate-900 dark:text-white mb-6">
                快速开始
              </h2>
              <p className="text-xl text-slate-600 dark:text-slate-300">
                几分钟内开始你的 Python 学习之旅
              </p>
            </div>
            <div className="bg-gradient-to-br from-slate-50 to-slate-100 dark:from-slate-800 dark:to-slate-700 rounded-3xl p-10 shadow-2xl border border-slate-200 dark:border-slate-600">
              <div className="grid md:grid-cols-2 gap-12">
                <div>
                  <h3 className="text-2xl font-bold text-slate-900 dark:text-white mb-6">
                    环境要求
                  </h3>
                  <ul className="space-y-4">
                    {[
                      "Python 3.8 或更高版本",
                      "pip（Python 包管理器）",
                      "VSCode（推荐编辑器）"
                    ].map((item, index) => (
                      <li key={index} className="flex items-center text-slate-600 dark:text-slate-300">
                        <div className="w-6 h-6 bg-gradient-to-r from-green-500 to-emerald-500 rounded-full flex items-center justify-center mr-4 shadow-lg">
                          <span className="text-white text-sm font-bold">✓</span>
                        </div>
                        {item}
                      </li>
                    ))}
                  </ul>
                </div>
                <div>
                  <h3 className="text-2xl font-bold text-slate-900 dark:text-white mb-6">
                    安装步骤
                  </h3>
                  <div className="bg-slate-900 text-slate-100 rounded-2xl p-6 font-mono text-sm shadow-xl border border-slate-700">
                    <div className="mb-3 text-slate-400"># 克隆项目</div>
                    <div className="text-blue-400 mb-1">git clone https://github.com/erweixin/js2py.git</div>
                    <div className="text-blue-400 mb-4">cd js2py</div>
                    <div className="mb-3 text-slate-400"># 安装依赖</div>
                    <div className="text-blue-400 mb-4">pnpm install</div>
                    <div className="mb-3 text-slate-400"># 启动开发服务器</div>
                    <div className="text-blue-400">pnpm dev</div>
                  </div>
                </div>
              </div>
              <div className="text-center mt-12">
                <Link
                  href="/docs/module-0-python-introduction"
                  className="group inline-flex items-center px-12 py-5 bg-gradient-to-r from-blue-600 to-purple-600 text-white font-bold rounded-2xl hover:from-blue-700 hover:to-purple-700 transition-all duration-300 transform hover:scale-105 shadow-2xl hover:shadow-blue-500/25 text-lg"
                >
                  立即开始学习
                  <span className="ml-3 group-hover:translate-x-1 transition-transform duration-300">→</span>
                </Link>
              </div>
            </div>
          </div>
        </div>
      </section>

      {/* 统计数据 */}
      <section className="py-24 bg-gradient-to-r from-blue-600 via-purple-600 to-pink-600 relative overflow-hidden">
        <div className="absolute inset-0 bg-[radial-gradient(circle_at_50%_50%,rgba(255,255,255,0.1),transparent_50%)]"></div>
        <div className="container mx-auto px-4 relative z-10">
          <div className="grid grid-cols-2 md:grid-cols-4 gap-12 text-center">
            {[
              { number: "12", label: "学习模块" },
              { number: "50+", label: "代码示例" },
              { number: "12+", label: "在线挑战" },
              { number: "4", label: "实战项目" }
            ].map((stat, index) => (
              <div key={index} className="group">
                <div className="text-5xl md:text-6xl font-bold text-white mb-3 group-hover:scale-110 transition-transform duration-300">
                  {stat.number}
                </div>
                <div className="text-blue-100 font-medium text-lg">{stat.label}</div>
              </div>
            ))}
          </div>
        </div>
      </section>

      {/* CTA 区域 */}
      <section className="py-24 bg-gradient-to-b from-slate-50 to-white dark:from-slate-900 dark:to-slate-800">
        <div className="container mx-auto px-4 text-center">
          <div className="max-w-4xl mx-auto">
            <h2 className="text-4xl md:text-5xl font-bold text-slate-900 dark:text-white mb-8">
              准备好开始你的 Python 学习之旅了吗？
            </h2>
            <p className="text-xl text-slate-600 dark:text-slate-300 mb-10 leading-relaxed">
              加入数千名前端开发者，一起掌握 Python 技能，构建全栈开发能力
            </p>
            <div className="flex flex-col sm:flex-row gap-6 justify-center">
              <Link
                href="/docs/module-0/getting-started"
                className="group inline-flex items-center px-12 py-5 bg-gradient-to-r from-blue-600 to-purple-600 text-white font-bold rounded-2xl hover:from-blue-700 hover:to-purple-700 transition-all duration-300 transform hover:scale-105 shadow-2xl hover:shadow-blue-500/25 text-lg"
              >
                <span className="mr-3">⭐</span>
                开始免费学习
                <span className="ml-3 group-hover:translate-x-1 transition-transform duration-300">→</span>
              </Link>
              <Link
                href="https://github.com/erweixin/js2py"
                className="group inline-flex items-center px-12 py-5 bg-slate-800 dark:bg-slate-700 text-white font-bold rounded-2xl hover:bg-slate-700 dark:hover:bg-slate-600 transition-all duration-300 border-2 border-slate-600 hover:border-slate-500 text-lg"
              >
                查看 GitHub
                <span className="ml-3 group-hover:translate-x-1 transition-transform duration-300">→</span>
              </Link>
            </div>
          </div>
        </div>
      </section>
    </main>
  )
}
