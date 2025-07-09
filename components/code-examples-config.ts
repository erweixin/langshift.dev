// 代码示例配置接口
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

// 语言配置接口
export interface LanguageConfig {
  value: string;
  label: string;
  icon: string;
  color: string;
  syntax: string;
  description?: string;
}

// 语言配置
export const languageConfigs: LanguageConfig[] = [
  {
    value: 'js',
    label: 'JavaScript',
    icon: '⚡',
    color: 'bg-yellow-500',
    syntax: 'javascript',
    description: '动态类型脚本语言'
  },
  {
    value: 'py',
    label: 'Python',
    icon: '🐍',
    color: 'bg-blue-500',
    syntax: 'python',
    description: '简洁优雅的编程语言'
  },
  {
    value: 'rust',
    label: 'Rust',
    icon: '🦀',
    color: 'bg-orange-500',
    syntax: 'rust',
    description: '内存安全的系统编程语言'
  },
];

// 代码示例配置
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

# 过滤、映射、归约
even_squares = list(map(lambda x: x**2, filter(lambda x: x % 2 == 0, numbers)))
print(even_squares)  # [4, 16, 36, 64, 100]

# 使用 reduce 计算乘积
product = reduce(mul, filter(lambda x: x % 2 == 0, numbers))
print(product)  # 3840`,
    rightCode: `// JavaScript 数组方法和函数式编程
const numbers = [1, 2, 3, 4, 5, 6, 7, 8, 9, 10];

// 数组方法链式调用
const squares = numbers
  .filter(x => x % 2 === 0)
  .map(x => x ** 2);
console.log(squares); // [4, 16, 36, 64, 100]

// 对象映射
const squareObj = Object.fromEntries(
  numbers
    .filter(x => x % 2 === 0)
    .map(x => [x, x ** 2])
);
console.log(squareObj); // {2: 4, 4: 16, 6: 36, 8: 64, 10: 100}

// 生成器函数
function* squareGenerator(arr) {
  for (const x of arr) {
    if (x % 2 === 0) {
      yield x ** 2;
    }
  }
}
console.log([...squareGenerator(numbers)]); // [4, 16, 36, 64, 100]

// 函数式编程
const evenSquares = numbers
  .filter(x => x % 2 === 0)
  .map(x => x ** 2);
console.log(evenSquares); // [4, 16, 36, 64, 100]

// 使用 reduce 计算乘积
const product = numbers
  .filter(x => x % 2 === 0)
  .reduce((acc, x) => acc * x, 1);
console.log(product); // 3840

// 现代 JavaScript 的管道操作符（提案）
// const result = numbers |> filter(x => x % 2 === 0) |> map(x => x ** 2);`,
    leftLanguage: 'python',
    rightLanguage: 'javascript',
    titleLeft: 'Python',
    titleRight: 'JavaScript',
    description: '函数式编程和数据处理对比 - 不同范式的实现',
    tags: ['函数式编程', '数据处理', '生成器', '链式调用'],
    difficulty: 'intermediate',
    category: '函数式编程'
  },
  'py-rust': {
    leftCode: `# Python 类定义和面向对象编程
class Person:
    def __init__(self, name, age):
        self.name = name
        self.age = age
    
    def greet(self):
        return f"Hello, I'm {self.name}"
    
    @property
    def is_adult(self):
        return self.age >= 18
    
    def __str__(self):
        return f"Person(name={self.name}, age={self.age})"
    
    def __repr__(self):
        return f"Person('{self.name}', {self.age})"

# 继承
class Student(Person):
    def __init__(self, name, age, student_id):
        super().__init__(name, age)
        self.student_id = student_id
    
    def greet(self):
        return f"Hi, I'm {self.name}, student #{self.student_id}"
    
    def study(self, subject):
        return f"{self.name} is studying {subject}"

# 多重继承
class TeachingAssistant(Student, Person):
    def __init__(self, name, age, student_id, course):
        Student.__init__(self, name, age, student_id)
        self.course = course
    
    def teach(self):
        return f"{self.name} is teaching {self.course}"

# 使用示例
person = Person("Alice", 30)
student = Student("Bob", 20, "12345")
ta = TeachingAssistant("Charlie", 25, "67890", "Python")

print(person.greet())
print(student.greet())
print(student.study("Computer Science"))
print(ta.teach())
print(person.is_adult)`,
    rightCode: `// Rust 结构体和面向对象编程
struct Person {
    name: String,
    age: u32,
}

impl Person {
    fn new(name: String, age: u32) -> Self {
        Person { name, age }
    }
    
    fn greet(&self) -> String {
        format!("Hello, I'm {}", self.name)
    }
    
    fn is_adult(&self) -> bool {
        self.age >= 18
    }
}

impl std::fmt::Display for Person {
    fn fmt(&self, f: &mut std::fmt::Formatter) -> std::fmt::Result {
        write!(f, "Person(name={}, age={})", self.name, self.age)
    }
}

impl std::fmt::Debug for Person {
    fn fmt(&self, f: &mut std::fmt::Formatter) -> std::fmt::Result {
        write!(f, "Person('{}', {})", self.name, self.age)
    }
}

// 继承通过组合实现
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
        format!("Hi, I'm {}, student #{}", self.person.name, self.student_id)
    }
    
    fn study(&self, subject: &str) -> String {
        format!("{} is studying {}", self.person.name, subject)
    }
}

// 使用 trait 实现多态
trait Teacher {
    fn teach(&self) -> String;
}

struct TeachingAssistant {
    student: Student,
    course: String,
}

impl TeachingAssistant {
    fn new(name: String, age: u32, student_id: String, course: String) -> Self {
        TeachingAssistant {
            student: Student::new(name, age, student_id),
            course,
        }
    }
}

impl Teacher for TeachingAssistant {
    fn teach(&self) -> String {
        format!("{} is teaching {}", self.student.person.name, self.course)
    }
}

// 使用示例
fn main() {
    let person = Person::new("Alice".to_string(), 30);
    let student = Student::new("Bob".to_string(), 20, "12345".to_string());
    let ta = TeachingAssistant::new("Charlie".to_string(), 25, "67890".to_string(), "Rust".to_string());
    
    println!("{}", person.greet());
    println!("{}", student.greet());
    println!("{}", student.study("Computer Science"));
    println!("{}", ta.teach());
    println!("{}", person.is_adult());
}`,
    leftLanguage: 'python',
    rightLanguage: 'rust',
    titleLeft: 'Python',
    titleRight: 'Rust',
    description: '面向对象编程和类系统对比 - 继承 vs 组合',
    tags: ['面向对象', '类', '继承', '组合', 'Trait'],
    difficulty: 'advanced',
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

fn divide(a: f64, b: f64) -> Result<f64, String> {
    if b == 0.0 {
        Err("Division by zero".to_string())
    } else {
        Ok(a / b)
    }
}

// 自定义错误类型
#[derive(Debug)]
enum AppError {
    FileError(io::Error),
    MathError(String),
    ValidationError(String),
}

impl std::fmt::Display for AppError {
    fn fmt(&self, f: &mut std::fmt::Formatter) -> std::fmt::Result {
        match self {
            AppError::FileError(e) => write!(f, "File error: {}", e),
            AppError::MathError(e) => write!(f, "Math error: {}", e),
            AppError::ValidationError(e) => write!(f, "Validation error: {}", e),
        }
    }
}

impl std::error::Error for AppError {}

// 使用示例
fn main() {
    // 处理文件读取
    match read_file_content("example.txt") {
        Ok(content) => println!("File content: {}", content),
        Err(e) => println!("Error reading file: {}", e),
    }
    
    // 处理除法运算
    match divide(10.0, 2.0) {
        Ok(result) => println!("Result: {}", result),
        Err(e) => println!("Error: {}", e),
    }
    
    // 使用 ? 操作符
    if let Err(e) = process_data() {
        println!("Processing error: {}", e);
    }
    
    // 使用 map_err 转换错误类型
    let result = divide(10.0, 0.0)
        .map_err(|e| AppError::MathError(e));
    
    // 使用 and_then 链式处理
    let file_result = read_file_content("config.txt")
        .and_then(|content| {
            if content.is_empty() {
                Err(io::Error::new(io::ErrorKind::InvalidData, "Empty file"))
            } else {
                Ok(content)
            }
        });
}

fn process_data() -> Result<(), AppError> {
    let result = divide(10.0, 0.0)
        .map_err(|e| AppError::MathError(e))?;
    println!("Processed: {}", result);
    Ok(())
}`,
    rightCode: `// JavaScript 错误处理和 Promise
const fs = require('fs').promises;

async function readFileContent(filename) {
  try {
    const content = await fs.readFile(filename, 'utf8');
    return content;
  } catch (error) {
    throw new Error(\`Error reading file: \${error.message}\`);
  }
}

function divide(a, b) {
  if (b === 0) {
    throw new Error("Division by zero");
  }
  return a / b;
}

// 自定义错误类
class AppError extends Error {
  constructor(message, type = 'AppError') {
    super(message);
    this.name = 'AppError';
    this.type = type;
  }
}

class FileError extends AppError {
  constructor(message) {
    super(message, 'FileError');
    this.name = 'FileError';
  }
}

class MathError extends AppError {
  constructor(message) {
    super(message, 'MathError');
    this.name = 'MathError';
  }
}

// 使用示例
async function main() {
  // 处理文件读取
  try {
    const content = await readFileContent('example.txt');
    console.log('File content:', content);
  } catch (error) {
    console.log('Error reading file:', error.message);
  }
  
  // 处理除法运算
  try {
    const result = divide(10, 2);
    console.log('Result:', result);
  } catch (error) {
    console.log('Error:', error.message);
  }
  
  // 使用 Promise 链式处理
  divide(10, 0)
    .then(result => console.log('Processed:', result))
    .catch(error => console.log('Processing error:', error.message));
  
  // 使用 async/await 和自定义错误
  try {
    const result = await processData();
    console.log('Success:', result);
  } catch (error) {
    if (error instanceof MathError) {
      console.log('Math error:', error.message);
    } else if (error instanceof FileError) {
      console.log('File error:', error.message);
    } else {
      console.log('Unknown error:', error.message);
    }
  }
}

// 现代 async/await 错误处理
async function processData() {
  try {
    const result = divide(10, 0);
    console.log('Processed:', result);
    return result;
  } catch (error) {
    throw new MathError(\`Processing failed: \${error.message}\`);
  }
}

// 使用 Promise.all 处理多个异步操作
async function processMultipleFiles(filenames) {
  try {
    const promises = filenames.map(filename => readFileContent(filename));
    const results = await Promise.all(promises);
    return results;
  } catch (error) {
    throw new FileError(\`Failed to process files: \${error.message}\`);
  }
}

main();`,
    leftLanguage: 'rust',
    rightLanguage: 'javascript',
    titleLeft: 'Rust',
    titleRight: 'JavaScript',
    description: '错误处理和异步编程对比 - Result vs Promise',
    tags: ['错误处理', '异步', 'Promise', 'Result', '自定义错误'],
    difficulty: 'advanced',
    category: '错误处理'
  },
  'rust-py': {
    leftCode: `// Rust 所有权系统和内存管理
fn main() {
    // 所有权转移
    let s1 = String::from("hello");
    let s2 = s1; // s1 的所有权移动到 s2
    // println!("{}", s1); // 编译错误！
    println!("{}", s2); // 正常工作
    
    // 借用
    let s3 = String::from("world");
    let len = calculate_length(&s3); // 借用 s3
    println!("Length of '{}' is {}", s3, len); // s3 仍然可用
    
    // 可变借用
    let mut s4 = String::from("hello");
    change(&mut s4);
    println!("{}", s4); // "hello, world"
    
    // 切片
    let s5 = String::from("hello world");
    let hello = &s5[0..5];
    let world = &s5[6..11];
    println!("{} {}", hello, world);
    
    // 生命周期
    let string1 = String::from("long string is long");
    let string2 = String::from("xyz");
    let result = longest(&string1, &string2);
    println!("The longest string is '{}'", result);
    
    // 智能指针
    let data = Box::new(42);
    println!("Boxed value: {}", data);
    
    // 引用计数
    use std::rc::Rc;
    let shared_data = Rc::new(String::from("shared"));
    let data1 = Rc::clone(&shared_data);
    let data2 = Rc::clone(&shared_data);
    println!("Reference count: {}", Rc::strong_count(&shared_data));
}

fn calculate_length(s: &String) -> usize {
    s.len()
}

fn change(some_string: &mut String) {
    some_string.push_str(", world");
}

fn longest<'a>(x: &'a str, y: &'a str) -> &'a str {
    if x.len() > y.len() {
        x
    } else {
        y
    }
}`,
    rightCode: `# Python 引用系统和内存管理
import sys
from typing import List, Optional

def main():
    # 对象引用
    s1 = "hello"
    s2 = s1  # s1 和 s2 指向同一个对象
    print(s1)  # 正常工作
    print(s2)  # 正常工作
    print(f"s1 is s2: {s1 is s2}")  # True
    
    # 字符串是不可变的
    s3 = "world"
    length = calculate_length(s3)  # 传递引用
    print(f"Length of '{s3}' is {length}")  # s3 仍然可用
    
    # 列表是可变的
    lst = ["hello"]
    change_list(lst)
    print(lst)  # ["hello", "world"]
    
    # 切片
    s5 = "hello world"
    hello = s5[0:5]
    world = s5[6:11]
    print(f"{hello} {world}")
    
    # 引用计数
    import gc
    x = [1, 2, 3]
    y = x  # 增加引用计数
    print(f"Reference count: {sys.getrefcount(x)}")
    
    # 垃圾回收
    del y  # 减少引用计数
    print(f"Reference count after del: {sys.getrefcount(x)}")
    
    # 弱引用
    import weakref
    class Cache:
        def __init__(self):
            self._cache = weakref.WeakValueDictionary()
        
        def get(self, key):
            return self._cache.get(key)
        
        def set(self, key, value):
            self._cache[key] = value
    
    cache = Cache()
    data = [1, 2, 3]
    cache.set("key", data)
    print(f"Cached data: {cache.get('key')}")
    
    # 内存管理示例
    class MemoryManager:
        def __init__(self):
            self._data = []
        
        def add_data(self, data):
            self._data.append(data)
            print(f"Added data, current size: {len(self._data)}")
        
        def clear_data(self):
            self._data.clear()
            print("Data cleared")
    
    manager = MemoryManager()
    for i in range(5):
        manager.add_data(f"data_{i}")
    manager.clear_data()

def calculate_length(s: str) -> int:
    return len(s)

def change_list(some_list: List[str]) -> None:
    some_list.append("world")

if __name__ == "__main__":
    main()`,
    leftLanguage: 'rust',
    rightLanguage: 'python',
    titleLeft: 'Rust',
    titleRight: 'Python',
    description: '内存管理和所有权系统对比 - 手动 vs 自动管理',
    tags: ['内存管理', '所有权', '引用', '生命周期', '垃圾回收'],
    difficulty: 'advanced',
    category: '内存管理'
  },
};

// 获取所有可用的语言组合
export const getAvailableCombinations = (): string[] => {
  return Object.keys(codeExamples);
};

// 根据难度获取示例
export const getExamplesByDifficulty = (difficulty: 'beginner' | 'intermediate' | 'advanced'): CodeExample[] => {
  return Object.values(codeExamples).filter(example => example.difficulty === difficulty);
};

// 根据分类获取示例
export const getExamplesByCategory = (category: string): CodeExample[] => {
  return Object.values(codeExamples).filter(example => example.category === category);
};

// 获取语言配置
export const getLanguageConfig = (value: string): LanguageConfig => {
  return languageConfigs.find(config => config.value === value) || languageConfigs[0];
};

// 获取所有语言配置
export const getAllLanguageConfigs = (): LanguageConfig[] => {
  return languageConfigs;
}; 