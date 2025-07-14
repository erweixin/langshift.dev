// 繁體中文代碼示例配置
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
    leftCode: `// JavaScript 遞歸函數
function factorial(n) {
  if (n === 0) return 1;
  return n * factorial(n - 1);
}

// 使用範例
console.log(factorial(5)); // 120

// 箭頭函數版本
const factorialArrow = (n) => n === 0 ? 1 : n * factorialArrow(n - 1);
console.log(factorialArrow(5)); // 120

// 尾遞歸優化版本
function factorialTail(n, acc = 1) {
  if (n === 0) return acc;
  return factorialTail(n - 1, n * acc);
}
console.log(factorialTail(5)); // 120`,
    rightCode: `# Python 遞歸函數
def factorial(n):
    if n == 0:
        return 1
    return n * factorial(n - 1)

# 使用範例
print(factorial(5))  # 120

# Lambda 函數版本
factorial_lambda = lambda n: 1 if n == 0 else n * factorial_lambda(n - 1)
print(factorial_lambda(5))  # 120

# 尾遞歸優化版本
def factorial_tail(n, acc=1):
    if n == 0:
        return acc
    return factorial_tail(n - 1, n * acc)
print(factorial_tail(5))  # 120`,
    leftLanguage: 'javascript',
    rightLanguage: 'python',
    titleLeft: 'JavaScript',
    titleRight: 'Python',
    description: '遞歸函數的語法對比 - 從基礎到優化',
    tags: ['函數', '遞歸', '基礎語法', '優化'],
    difficulty: 'beginner',
    category: '函數編程'
  },
  'js-rust': {
    leftCode: `// JavaScript 函數和類型系統
function sum(a, b) {
  return a + b;
}

// 使用範例
console.log(sum(5, 3)); // 8

// 箭頭函數
const multiply = (a, b) => a * b;
console.log(multiply(4, 6)); // 24

// 物件解構
const person = { name: 'Alice', age: 30 };
const { name, age } = person;
console.log(name, age);

// 陣列解構
const [first, second, ...rest] = [1, 2, 3, 4, 5];
console.log(first, second, rest);

// 預設參數
function greet(name = 'World', greeting = 'Hello') {
  return \`\${greeting}, \${name}!\`;
}
console.log(greet()); // "Hello, World!"
console.log(greet('Alice', 'Hi')); // "Hi, Alice!"`,
    rightCode: `// Rust 函數和類型系統
fn sum(a: i32, b: i32) -> i32 {
    a + b
}

// 使用範例
fn main() {
    println!("{}", sum(5, 3)); // 8
    
    // 閉包
    let multiply = |a: i32, b: i32| a * b;
    println!("{}", multiply(4, 6)); // 24
    
    // 結構體解構
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
    
    // 陣列解構
    let arr = [1, 2, 3, 4, 5];
    let [first, second, ..rest] = arr;
    println!("{} {} {:?}", first, second, rest);
    
    // 預設參數透過 Option 實現
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
    description: '函數定義和類型系統對比 - 動態 vs 靜態類型',
    tags: ['函數', '類型', '解構', '預設參數'],
    difficulty: 'intermediate',
    category: '函數編程'
  },
  'py-js': {
    leftCode: `# Python 列表推導式和函數式編程
numbers = [1, 2, 3, 4, 5, 6, 7, 8, 9, 10]

# 列表推導式
squares = [x**2 for x in numbers if x % 2 == 0]
print(squares)  # [4, 16, 36, 64, 100]

# 字典推導式
square_dict = {x: x**2 for x in numbers if x % 2 == 0}
print(square_dict)  # {2: 4, 4: 16, 6: 36, 8: 64, 10: 100}

# 生成器表達式
squares_gen = (x**2 for x in numbers if x % 2 == 0)
print(list(squares_gen))  # [4, 16, 36, 64, 100]

# 函數式編程
from functools import reduce
from operator import mul

# Map 和 filter
even_squares = list(map(lambda x: x**2, filter(lambda x: x % 2 == 0, numbers)))
print(even_squares)  # [4, 16, 36, 64, 100]

# Reduce
product = reduce(mul, numbers, 1)
print(product)  # 3628800`,
    rightCode: `// JavaScript 陣列方法和函數式編程
const numbers = [1, 2, 3, 4, 5, 6, 7, 8, 9, 10];

// 陣列方法（相當於列表推導式）
const squares = numbers.filter(x => x % 2 === 0).map(x => x ** 2);
console.log(squares); // [4, 16, 36, 64, 100]

// 物件創建（相當於字典推導式）
const squareDict = Object.fromEntries(
  numbers.filter(x => x % 2 === 0).map(x => [x, x ** 2])
);
console.log(squareDict); // {2: 4, 4: 16, 6: 36, 8: 64, 10: 100}

// 生成器函數（相當於生成器表達式）
function* squareGenerator(arr) {
  for (const x of arr) {
    if (x % 2 === 0) {
      yield x ** 2;
    }
  }
}
const squaresGen = Array.from(squareGenerator(numbers));
console.log(squaresGen); // [4, 16, 36, 64, 100]

// 函數式編程與 reduce
const evenSquares = numbers
  .filter(x => x % 2 === 0)
  .map(x => x ** 2);
console.log(evenSquares); // [4, 16, 36, 64, 100]

// Reduce 計算乘積
const product = numbers.reduce((acc, x) => acc * x, 1);
console.log(product); // 3628800`,
    leftLanguage: 'python',
    rightLanguage: 'javascript',
    titleLeft: 'Python',
    titleRight: 'JavaScript',
    description: '列表推導式 vs 陣列方法 - 函數式編程模式',
    tags: ['陣列', '函數式編程', '推導式', '生成器'],
    difficulty: 'intermediate',
    category: '資料結構'
  },
  'py-rust': {
    leftCode: `# Python 類和面向物件編程
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

# 繼承
class Student(Person):
    def __init__(self, name: str, age: int, student_id: str):
        super().__init__(name, age)
        self.student_id = student_id
    
    def greet(self) -> str:
        return f"Hi, I'm {self.name}, a student with ID {self.student_id}"

# 使用範例
person = Person("Alice", 25)
print(person.greet())  # Hello, my name is Alice and I'm 25 years old

student = Student("Bob", 20, "S12345")
print(student.greet())  # Hi, I'm Bob, a student with ID S12345`,
    rightCode: `// Rust 結構體和面向物件模式
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

// 實現 Display trait 用於字串表示
impl std::fmt::Display for Person {
    fn fmt(&self, f: &mut std::fmt::Formatter) -> std::fmt::Result {
        write!(f, "Person(name='{}', age={})", self.name, self.age)
    }
}

// 透過組合實現繼承
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

// 使用範例
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
    description: '面向物件編程模式 - 類 vs 結構體和特徵',
    tags: ['面向物件', '類', '繼承', '結構體', '特徵'],
    difficulty: 'intermediate',
    category: '面向物件編程'
  },
  'rust-js': {
    leftCode: `// Rust 錯誤處理和 Result 類型
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
    // 使用 match 進行錯誤處理
    match read_file_content("data.txt") {
        Ok(content) => {
            match process_data(&content) {
                Ok(numbers) => println!("Processed numbers: {:?}", numbers),
                Err(e) => eprintln!("Processing error: {}", e),
            }
        }
        Err(e) => eprintln!("File error: {}", e),
    }
    
    // 使用 ? 操作符進行更簡潔的錯誤處理
    fn process_file(filename: &str) -> Result<Vec<i32>, Box<dyn std::error::Error>> {
        let content = read_file_content(filename)?;
        let numbers = process_data(&content)?;
        Ok(numbers)
    }
}`,
    rightCode: `// JavaScript 錯誤處理和 async/await
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
    // 使用 try-catch 進行錯誤處理
    const content = await readFileContent('data.txt');
    const numbers = processData(content);
    console.log('Processed numbers:', numbers);
  } catch (error) {
    console.error('Error:', error.message);
  }
}

// 使用 async/await 進行更簡潔的錯誤處理
async function processFile(filename) {
  try {
    const content = await readFileContent(filename);
    return processData(content);
  } catch (error) {
    throw new Error(\`File processing failed: \${error.message}\`);
  }
}

// 呼叫主函數
main();`,
    leftLanguage: 'rust',
    rightLanguage: 'javascript',
    titleLeft: 'Rust',
    titleRight: 'JavaScript',
    description: '錯誤處理模式 - Result 類型 vs try-catch',
    tags: ['錯誤處理', 'Result 類型', 'Async/Await', '檔案 I/O'],
    difficulty: 'advanced',
    category: '錯誤處理'
  },
  'js-cpp': {
    leftCode: `// JavaScript: 動態類型
// 同一個函數可以接受不同類型的參數
function add(a, b) {
  return a + b;
}

// 1. 用於數字加法
console.log('5 + 10 =', add(5, 10));

// 2. 用於字串拼接
console.log('"Hello, " + "World!" =', add('Hello, ', 'World!'));`,
    rightCode: `#include <iostream>
#include <string>

// C++: 靜態類型與模板
// 使用模板來為不同類型生成專用的函數
template <typename T>
T add(T a, T b) {
  return a + b;
}

int main() {
  // 1. 用於整數加法 (T 被推導為 int)
  std::cout << "5 + 10 = " << add(5, 10) << std::endl;

  // 2. 用於字串拼接 (T 被推導為 std::string)
  std::cout << "\"Hello, \" + \"World!\" = " 
            << add(std::string("Hello, "), std::string("World!")) 
            << std::endl;
            
  return 0;
}`,
    leftLanguage: 'javascript',
    rightLanguage: 'cpp',
    titleLeft: 'JavaScript',
    titleRight: 'C++',
    description: '函數定義和類型系統的比較，突顯 JavaScript 的動態類型與 C++ 的靜態類型和模板。',
    tags: ['類型系統', '函數', '模板', '動態 vs 靜態'],
    difficulty: 'beginner',
    category: '核心概念'
  }
};