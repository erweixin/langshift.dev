import UniversalEditor from '@/components/universal-editor'

export default function TestUniversalEditorPage() {
  return (
    <div className="min-h-screen bg-gray-50 dark:bg-gray-900">
      <div className="container mx-auto py-8 space-y-8">
        <h1 className="text-2xl font-bold text-gray-900 dark:text-white">
          通用编辑器测试 - Pyodide 按需加载
        </h1>
        
        {/* 测试 1: 只有 JavaScript 代码 - 不应该加载 Pyodide */}
        <div>
          <h2 className="text-lg font-semibold text-gray-800 dark:text-gray-200 mb-4">
            测试 1: 只有 JavaScript 代码（不应该加载 Pyodide）
          </h2>
          <UniversalEditor
            title="JavaScript 测试"
            code={[
              {
                lang: 'javascript',
                value: `console.log('Hello from JavaScript!');
const numbers = [1, 2, 3, 4, 5];
const doubled = numbers.map(n => n * 2);
console.log('Doubled numbers:', doubled);`
              }
            ]}
            height={200}
          />
        </div>

        {/* 测试 2: 只有 Python 代码 - 应该按需加载 Pyodide */}
        <div>
          <h2 className="text-lg font-semibold text-gray-800 dark:text-gray-200 mb-4">
            测试 2: 只有 Python 代码（应该按需加载 Pyodide）
          </h2>
          <UniversalEditor
            title="Python 测试"
            code={[
              {
                lang: 'python',
                value: `print('Hello from Python!')
numbers = [1, 2, 3, 4, 5]
doubled = [n * 2 for n in numbers]
print('Doubled numbers:', doubled)`
              }
            ]}
            height={200}
          />
        </div>

        {/* 测试 3: 对比模式 - 应该按需加载两个运行时 */}
        <div>
          <h2 className="text-lg font-semibold text-gray-800 dark:text-gray-200 mb-4">
            测试 3: 对比模式（应该按需加载两个运行时）
          </h2>
          <UniversalEditor
            title="JavaScript vs Python 对比"
            compare={true}
            code={[
              {
                lang: 'javascript',
                value: `// JavaScript 实现
function fibonacci(n) {
  if (n <= 1) return n;
  return fibonacci(n - 1) + fibonacci(n - 2);
}

console.log('Fibonacci(10):', fibonacci(10));`
              },
              {
                lang: 'python',
                value: `# Python 实现
def fibonacci(n):
    if n <= 1:
        return n
    return fibonacci(n - 1) + fibonacci(n - 2)

print('Fibonacci(10):', fibonacci(10))`
              }
            ]}
            height={250}
          />
        </div>

        {/* 测试 4: Python 使用外部库 - 测试动态加载 */}
        <div>
          <h2 className="text-lg font-semibold text-gray-800 dark:text-gray-200 mb-4">
            测试 4: Python 使用外部库（测试动态加载）
          </h2>
          <UniversalEditor
            title="Python 外部库测试"
            code={[
              {
                lang: 'python',
                value: `import numpy as np
import json

# 使用 numpy 创建数组
arr = np.array([1, 2, 3, 4, 5])
print('Numpy array:', arr)
print('Array sum:', np.sum(arr))

# 使用 json
data = {'name': 'test', 'values': [1, 2, 3]}
print('JSON data:', json.dumps(data, indent=2))`
              }
            ]}
            height={250}
          />
        </div>
      </div>
    </div>
  )
} 