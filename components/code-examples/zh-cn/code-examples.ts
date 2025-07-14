// 简体中文代码示例配置
export interface CodeExample {
  leftCode: string;
  rightCode: string;
  leftLanguage: string;
  rightLanguage: string;
  titleLeft: string;
  titleRight: string;
  description?: string;
  tags?: string[];
  difficulty?: 'beginner' | 'intermediate' | 'advanced';
  category?: string;
}

export const codeExamples: Record<string, CodeExample> = {
  'js-py': {
    leftCode: `// JavaScript 递归函数
function factorial(n) {
  if (n === 0) return 1;
  return n * factorial(n - 1);
}

// 使用示例
console.log(factorial(5)); // 120

// 箭头函数版本
const factorialArrow = (n) => n === 0 ? 1 : n * factorialArrow(n - 1);
console.log(factorialArrow(5)); // 120

// 尾递归优化版本
function factorialTail(n, acc = 1) {
  if (n === 0) return acc;
  return factorialTail(n - 1, n * acc);
}
console.log(factorialTail(5)); // 120`,
    rightCode: `# Python 递归函数
def factorial(n):
    if n == 0:
        return 1
    return n * factorial(n - 1)

# 使用示例
print(factorial(5))  # 120

# Lambda 函数版本
factorial_lambda = lambda n: 1 if n == 0 else n * factorial_lambda(n - 1)
print(factorial_lambda(5))  # 120

# 尾递归优化版本
def factorial_tail(n, acc=1):
    if n == 0:
        return acc
    return factorial_tail(n - 1, n * acc)
print(factorial_tail(5))  # 120`,
    leftLanguage: 'javascript',
    rightLanguage: 'python',
    titleLeft: 'JavaScript',
    titleRight: 'Python',
    description: '递归函数的语法对比 - 从基础到优化',
    tags: ['函数', '递归', '基础语法', '优化'],
    difficulty: 'beginner',
    category: '函数编程'
  },
  'js-rust': {
    leftCode: `// JavaScript 函数和类型系统
function sum(a, b) {
  return a + b;
}

// 使用示例
console.log(sum(5, 3)); // 8

// 箭头函数
const multiply = (a, b) => a * b;
console.log(multiply(4, 6)); // 24

// 对象解构
const person = { name: 'Alice', age: 30 };
const { name, age } = person;
console.log(name, age);

// 数组解构
const [first, second, ...rest] = [1, 2, 3, 4, 5];
console.log(first, second, rest);

// 默认参数
function greet(name = 'World', greeting = 'Hello') {
  return \`\${greeting}, \${name}!\`;
}
console.log(greet()); // "Hello, World!"
console.log(greet('Alice', 'Hi')); // "Hi, Alice!"`,
    rightCode: `// Rust 函数和类型系统
fn sum(a: i32, b: i32) -> i32 {
    a + b
}

// 使用示例
fn main() {
    println!("{}", sum(5, 3)); // 8
    
    // 闭包
    let multiply = |a: i32, b: i32| a * b;
    println!("{}", multiply(4, 6)); // 24
    
    // 结构体解构
    struct Person {
        name: String,
        age: u32,
    }
    
    let person = Person {
        name: String::from("Alice"),
        age: 30,
    };
    
    let Person { name, age } = person;
    println!("{} {}", name, age);
    
    // 数组解构
    let arr = [1, 2, 3, 4, 5];
    let [first, second, ..rest] = arr;
    println!("{} {} {:?}", first, second, rest);
    
    // 默认参数通过 Option 实现
    fn greet(name: Option<&str>, greeting: Option<&str>) -> String {
        let name = name.unwrap_or("World");
        let greeting = greeting.unwrap_or("Hello");
        format!("{}, {}!", greeting, name)
    }
    
    println!("{}", greet(None, None)); // "Hello, World!"
    println!("{}", greet(Some("Alice"), Some("Hi"))); // "Hi, Alice!"
}`,
    leftLanguage: 'javascript',
    rightLanguage: 'rust',
    titleLeft: 'JavaScript',
    titleRight: 'Rust',
    description: '函数定义和类型系统对比 - 动态 vs 静态类型',
    tags: ['函数', '类型', '解构', '默认参数'],
    difficulty: 'intermediate',
    category: '函数编程'
  },
  'py-js': {
    leftCode: `# Python 列表推导式和函数式编程
numbers = [1, 2, 3, 4, 5, 6, 7, 8, 9, 10]

# 列表推导式
squares = [x**2 for x in numbers if x % 2 == 0]
print(squares)  # [4, 16, 36, 64, 100]

# 字典推导式
square_dict = {x: x**2 for x in numbers if x % 2 == 0}
print(square_dict)  # {2: 4, 4: 16, 6: 36, 8: 64, 10: 100}

# 生成器表达式
squares_gen = (x**2 for x in numbers if x % 2 == 0)
print(list(squares_gen))  # [4, 16, 36, 64, 100]

# 函数式编程
from functools import reduce
from operator import mul

# Map 和 filter
even_squares = list(map(lambda x: x**2, filter(lambda x: x % 2 == 0, numbers)))
print(even_squares)  # [4, 16, 36, 64, 100]

# Reduce
product = reduce(mul, numbers, 1)
print(product)  # 3628800`,
    rightCode: `// JavaScript 数组方法和函数式编程
const numbers = [1, 2, 3, 4, 5, 6, 7, 8, 9, 10];

// 数组方法（相当于列表推导式）
const squares = numbers.filter(x => x % 2 === 0).map(x => x ** 2);
console.log(squares); // [4, 16, 36, 64, 100]

// 对象创建（相当于字典推导式）
const squareDict = Object.fromEntries(
  numbers.filter(x => x % 2 === 0).map(x => [x, x ** 2])
);
console.log(squareDict); // {2: 4, 4: 16, 6: 36, 8: 64, 10: 100}

// 生成器函数（相当于生成器表达式）
function* squareGenerator(arr) {
  for (const x of arr) {
    if (x % 2 === 0) {
      yield x ** 2;
    }
  }
}
const squaresGen = Array.from(squareGenerator(numbers));
console.log(squaresGen); // [4, 16, 36, 64, 100]

// 函数式编程与 reduce
const evenSquares = numbers
  .filter(x => x % 2 === 0)
  .map(x => x ** 2);
console.log(evenSquares); // [4, 16, 36, 64, 100]

// Reduce 计算乘积
const product = numbers.reduce((acc, x) => acc * x, 1);
console.log(product); // 3628800`,
    leftLanguage: 'python',
    rightLanguage: 'javascript',
    titleLeft: 'Python',
    titleRight: 'JavaScript',
    description: '列表推导式 vs 数组方法 - 函数式编程模式',
    tags: ['数组', '函数式编程', '推导式', '生成器'],
    difficulty: 'intermediate',
    category: '数据结构'
  },
  'py-rust': {
    leftCode: `# Python 类和面向对象编程
class Person:
    def __init__(self, name: str, age: int):
        self.name = name
        self.age = age
    
    def greet(self) -> str:
        return f"Hello, my name is {self.name} and I'm {self.age} years old"
    
    def __str__(self) -> str:
        return f"Person(name='{self.name}', age={self.age})"
    
    def __repr__(self) -> str:
        return self.__str__()

# 继承
class Student(Person):
    def __init__(self, name: str, age: int, student_id: str):
        super().__init__(name, age)
        self.student_id = student_id
    
    def greet(self) -> str:
        return f"Hi, I'm {self.name}, a student with ID {self.student_id}"

# 使用示例
person = Person("Alice", 25)
print(person.greet())  # Hello, my name is Alice and I'm 25 years old

student = Student("Bob", 20, "S12345")
print(student.greet())  # Hi, I'm Bob, a student with ID S12345`,
    rightCode: `// Rust 结构体和面向对象模式
struct Person {
    name: String,
    age: u32,
}

impl Person {
    fn new(name: String, age: u32) -> Self {
        Person { name, age }
    }
    
    fn greet(&self) -> String {
        format!("Hello, my name is {} and I'm {} years old", self.name, self.age)
    }
}

// 实现 Display trait 用于字符串表示
impl std::fmt::Display for Person {
    fn fmt(&self, f: &mut std::fmt::Formatter) -> std::fmt::Result {
        write!(f, "Person(name='{}', age={})", self.name, self.age)
    }
}

// 通过组合实现继承
struct Student {
    person: Person,
    student_id: String,
}

impl Student {
    fn new(name: String, age: u32, student_id: String) -> Self {
        Student {
            person: Person::new(name, age),
            student_id,
        }
    }
    
    fn greet(&self) -> String {
        format!("Hi, I'm {}, a student with ID {}", self.person.name, self.student_id)
    }
}

// 使用示例
fn main() {
    let person = Person::new("Alice".to_string(), 25);
    println!("{}", person.greet()); // Hello, my name is Alice and I'm 25 years old
    
    let student = Student::new("Bob".to_string(), 20, "S12345".to_string());
    println!("{}", student.greet()); // Hi, I'm Bob, a student with ID S12345
}`,
    leftLanguage: 'python',
    rightLanguage: 'rust',
    titleLeft: 'Python',
    titleRight: 'Rust',
    description: '面向对象编程模式 - 类 vs 结构体和特征',
    tags: ['面向对象', '类', '继承', '结构体', '特征'],
    difficulty: 'intermediate',
    category: '面向对象编程'
  },
  'rust-js': {
    leftCode: `// Rust 错误处理和 Result 类型
use std::fs::File;
use std::io::{self, Read};

fn read_file_content(filename: &str) -> Result<String, io::Error> {
    let mut file = File::open(filename)?;
    let mut contents = String::new();
    file.read_to_string(&mut contents)?;
    Ok(contents)
}

fn process_data(data: &str) -> Result<Vec<i32>, String> {
    data.lines()
        .map(|line| line.trim().parse::<i32>())
        .collect::<Result<Vec<i32>, _>>()
        .map_err(|e| format!("Failed to parse number: {}", e))
}

fn main() {
    // 使用 match 进行错误处理
    match read_file_content("data.txt") {
        Ok(content) => {
            match process_data(&content) {
                Ok(numbers) => println!("Processed numbers: {:?}", numbers),
                Err(e) => eprintln!("Processing error: {}", e),
            }
        }
        Err(e) => eprintln!("File error: {}", e),
    }
    
    // 使用 ? 操作符进行更简洁的错误处理
    fn process_file(filename: &str) -> Result<Vec<i32>, Box<dyn std::error::Error>> {
        let content = read_file_content(filename)?;
        let numbers = process_data(&content)?;
        Ok(numbers)
    }
}`,
    rightCode: `// JavaScript 错误处理和 async/await
const fs = require('fs').promises;

async function readFileContent(filename) {
  try {
    const content = await fs.readFile(filename, 'utf8');
    return content;
  } catch (error) {
    throw new Error(\`Failed to read file: \${error.message}\`);
  }
}

function processData(data) {
  try {
    return data.split('\\n')
      .map(line => line.trim())
      .filter(line => line.length > 0)
      .map(line => {
        const num = parseInt(line, 10);
        if (isNaN(num)) {
          throw new Error(\`Invalid number: \${line}\`);
        }
        return num;
      });
  } catch (error) {
    throw new Error(\`Failed to process data: \${error.message}\`);
  }
}

async function main() {
  try {
    // 使用 try-catch 进行错误处理
    const content = await readFileContent('data.txt');
    const numbers = processData(content);
    console.log('Processed numbers:', numbers);
  } catch (error) {
    console.error('Error:', error.message);
  }
}

// 使用 async/await 进行更简洁的错误处理
async function processFile(filename) {
  try {
    const content = await readFileContent(filename);
    return processData(content);
  } catch (error) {
    throw new Error(\`File processing failed: \${error.message}\`);
  }
}

// 调用主函数
main();`,
    leftLanguage: 'rust',
    rightLanguage: 'javascript',
    titleLeft: 'Rust',
    titleRight: 'JavaScript',
    description: '错误处理模式 - Result 类型 vs try-catch',
    tags: ['错误处理', 'Result 类型', 'Async/Await', '文件 I/O'],
    difficulty: 'advanced',
    category: '错误处理'
  },
  'js-cpp': {
    leftCode: `// JavaScript: 动态类型
// 同一个函数可以接受不同类型的参数
function add(a, b) {
  return a + b;
}

// 1. 用于数字加法
console.log('5 + 10 =', add(5, 10));

// 2. 用于字符串拼接
console.log('"Hello, " + "World!" =', add('Hello, ', 'World!'));`,
    rightCode: `#include <iostream>
#include <string>

// C++: 静态类型与模板
// 使用模板来为不同类型生成专用的函数
template <typename T>
T add(T a, T b) {
  return a + b;
}

int main() {
  // 1. 用于整数加法 (T 被推导为 int)
  std::cout << "5 + 10 = " << add(5, 10) << std::endl;

  // 2. 用于字符串拼接 (T 被推导为 std::string)
  std::cout << "\"Hello, \" + \"World!\" = " 
            << add(std::string("Hello, "), std::string("World!")) 
            << std::endl;
            
  return 0;
}`,
    leftLanguage: 'javascript',
    rightLanguage: 'cpp',
    titleLeft: 'JavaScript',
    titleRight: 'C++',
    description: '函数定义和类型系统的比较，突出 JavaScript 的动态类型与 C++ 的静态类型和模板。',
    tags: ['类型系统', '函数', '模板', '动态 vs 静态'],
    difficulty: 'beginner',
    category: '核心概念'
  }
};