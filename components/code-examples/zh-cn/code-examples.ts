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
  },
  'js-go': {
    leftCode: `// JavaScript 异步编程：Promise 和 async/await
// 模拟异步操作
function fetchData() {
  return new Promise((resolve) => {
    setTimeout(() => {
      resolve({ id: 1, name: 'Alice' });
    }, 1000);
  });
}

// 使用 async/await
async function getUser() {
  console.log('获取用户数据...');
  const user = await fetchData();
  console.log('用户:', user);
  return user;
}

// 使用 Promise.then()
fetchData().then(user => {
  console.log('用户:', user);
});

// 并发请求
Promise.all([
  fetchData(),
  fetchData(),
]).then(users => {
  console.log('多个用户:', users);
});`,
    rightCode: `// Go 并发编程：Goroutines 和 Channels
package main

import (
  "fmt"
  "time"
)

// 模拟异步操作
func fetchData(userChan chan<- map[string]interface{}) {
  time.Sleep(time.Second)
  userChan <- map[string]interface{}{
    "id":   1,
    "name": "Alice",
  }
}

// 使用 goroutine 和 channel
func getUser() {
  fmt.Println("获取用户数据...")
  userChan := make(chan map[string]interface{})
  go fetchData(userChan)
  user := <-userChan
  fmt.Println("用户:", user)
}

// 使用 goroutine 和 channel
func main() {
  // 单个请求
  userChan := make(chan map[string]interface{})
  go fetchData(userChan)
  user := <-userChan
  fmt.Println("用户:", user)

  // 并发请求（类似 Promise.all）
  userChan1 := make(chan map[string]interface{})
  userChan2 := make(chan map[string]interface{})
  go fetchData(userChan1)
  go fetchData(userChan2)
  user1 := <-userChan1
  user2 := <-userChan2
  fmt.Println("多个用户:", []map[string]interface{}{user1, user2})
}`,
    leftLanguage: 'javascript',
    rightLanguage: 'go',
    titleLeft: 'JavaScript',
    titleRight: 'Go',
    description: '异步编程模式对比 - Promise/async-await vs Goroutines/Channels',
    tags: ['异步编程', '并发', 'Promise', 'Goroutines'],
    difficulty: 'intermediate',
    category: '异步编程'
  },
  'js-swift': {
    leftCode: `// JavaScript 数组和高阶函数
const numbers = [1, 2, 3, 4, 5];

// Map - 转换数组
const doubled = numbers.map(x => x * 2);
console.log(doubled); // [2, 4, 6, 8, 10]

// Filter - 过滤数组
const evens = numbers.filter(x => x % 2 === 0);
console.log(evens); // [2, 4]

// Reduce - 归约数组
const sum = numbers.reduce((acc, x) => acc + x, 0);
console.log(sum); // 15

// 链式调用
const result = numbers
  .filter(x => x % 2 === 0)
  .map(x => x * 2)
  .reduce((acc, x) => acc + x, 0);
console.log(result); // 12

// Find - 查找元素
const found = numbers.find(x => x > 3);
console.log(found); // 4`,
    rightCode: `// Swift 数组和高阶函数
import Foundation

let numbers = [1, 2, 3, 4, 5]

// Map - 转换数组
let doubled = numbers.map { $0 * 2 }
print(doubled) // [2, 4, 6, 8, 10]

// Filter - 过滤数组
let evens = numbers.filter { $0 % 2 == 0 }
print(evens) // [2, 4]

// Reduce - 归约数组
let sum = numbers.reduce(0) { $0 + $1 }
print(sum) // 15

// 链式调用
let result = numbers
  .filter { $0 % 2 == 0 }
  .map { $0 * 2 }
  .reduce(0) { $0 + $1 }
print(result) // 12

// Find - 查找元素
let found = numbers.first { $0 > 3 }
print(found as Any) // Optional(4)

// CompactMap - 处理可选值
let strings = ["1", "2", "abc", "4"]
let numbersFromStrings = strings.compactMap { Int($0) }
print(numbersFromStrings) // [1, 2, 4]`,
    leftLanguage: 'javascript',
    rightLanguage: 'swift',
    titleLeft: 'JavaScript',
    titleRight: 'Swift',
    description: '数组和函数式编程 - 高阶函数和链式调用',
    tags: ['数组', '函数式编程', '高阶函数', 'Map/Filter/Reduce'],
    difficulty: 'beginner',
    category: '数据结构'
  },
  'js-c': {
    leftCode: `// JavaScript: 动态数组和对象
// 数组可以动态增长
const arr = [1, 2, 3];
arr.push(4);
arr.push(5);
console.log(arr); // [1, 2, 3, 4, 5]

// 数组可以存储任意类型
arr.push("hello");
arr.push({ name: "Alice" });
console.log(arr); // [1, 2, 3, 4, 5, "hello", { name: "Alice" }]

// 对象动态添加属性
const obj = {};
obj.name = "Alice";
obj.age = 30;
obj.greet = function() {
  return \`Hello, I'm \${this.name}\`;
};
console.log(obj.greet()); // "Hello, I'm Alice"

// 自动内存管理
let data = [];
for (let i = 0; i < 1000; i++) {
  data.push({ id: i, value: Math.random() });
}
// 垃圾回收器会自动清理内存`,
    rightCode: `#include <stdio.h>
#include <stdlib.h>
#include <string.h>

// C: 手动内存管理和固定大小数组
// 数组大小必须固定
int arr[5] = {1, 2, 3, 4, 5};

// 动态数组需要手动管理内存
int* create_dynamic_array(int size) {
  int* arr = (int*)malloc(size * sizeof(int));
  if (arr == NULL) {
    fprintf(stderr, "内存分配失败\\n");
    exit(1);
  }
  return arr;
}

// 结构体定义
struct Person {
  char name[50];
  int age;
};

// 结构体数组
struct Person* create_person_array(int size) {
  struct Person* people = (struct Person*)malloc(size * sizeof(struct Person));
  if (people == NULL) {
    fprintf(stderr, "内存分配失败\\n");
    exit(1);
  }
  return people;
}

int main() {
  // 动态数组示例
  int size = 1000;
  int* data = create_dynamic_array(size);

  for (int i = 0; i < size; i++) {
    data[i] = i;
  }

  // 使用完必须手动释放内存
  free(data);

  // 结构体数组示例
  struct Person* people = create_person_array(10);
  strcpy(people[0].name, "Alice");
  people[0].age = 30;

  printf("Name: %s, Age: %d\\n", people[0].name, people[0].age);

  // 手动释放内存
  free(people);

  return 0;
}`,
    leftLanguage: 'javascript',
    rightLanguage: 'c',
    titleLeft: 'JavaScript',
    titleRight: 'C',
    description: '内存管理对比 - 垃圾回收 vs 手动内存管理',
    tags: ['内存管理', '数组', '动态 vs 静态', '指针'],
    difficulty: 'advanced',
    category: '内存管理'
  },
  'js-kt': {
    leftCode: `// JavaScript 类和面向对象编程
class Person {
  constructor(name, age) {
    this.name = name;
    this.age = age;
  }

  greet() {
    return \`Hello, I'm \${this.name}\`;
  }

  introduce() {
    return \`I'm \${this.age} years old\`;
  }
}

// 继承
class Student extends Person {
  constructor(name, age, studentId) {
    super(name, age);
    this.studentId = studentId;
  }

  greet() {
    return \`Hi, I'm \${this.name} (ID: \${this.studentId})\`;
  }
}

// 使用
const person = new Person("Alice", 25);
console.log(person.greet()); // "Hello, I'm Alice"

const student = new Student("Bob", 20, "S12345");
console.log(student.greet()); // "Hi, I'm Bob (ID: S12345)"`,
    rightCode: `// Kotlin 类和面向对象编程
// 数据类 - 自动生成 equals, hashCode, toString, copy
data class Person(
  val name: String,
  val age: Int
) {
  fun greet() = "Hello, I'm $name"

  fun introduce() = "I'm $age years old"
}

// 密封类 - 受限的类继承结构
sealed class StudentStatus {
  data class Active(val gpa: Double) : StudentStatus()
  data class OnProbation(val reason: String) : StudentStatus()
  object Graduated : StudentStatus()
}

// 继承
open class Animal(val name: String) {
  open fun makeSound() = "Some sound"
}

class Dog(name: String) : Animal(name) {
  override fun makeSound() = "Woof!"
}

// 使用
fun main() {
  val person = Person("Alice", 25)
  println(person.greet()) // "Hello, I'm Alice"

  // 数据类的 copy 功能
  val olderPerson = person.copy(age = 26)
  println(olderPerson) // "Person(name=Alice, age=26)"

  val dog = Dog("Buddy")
  println(dog.makeSound()) // "Woof!"

  // 智能类型转换
  fun checkStatus(status: StudentStatus) = when (status) {
    is StudentStatus.Active -> "Active with GPA \${status.gpa}"
    is StudentStatus.OnProbation -> "On probation: \${status.reason}"
    is StudentStatus.Graduated -> "Graduated"
  }
}`,
    leftLanguage: 'javascript',
    rightLanguage: 'kotlin',
    titleLeft: 'JavaScript',
    titleRight: 'Kotlin',
    description: '面向对象编程对比 - 类继承 vs 数据类和密封类',
    tags: ['面向对象', '类', '继承', '数据类'],
    difficulty: 'intermediate',
    category: '面向对象编程'
  },
  'js-java': {
    leftCode: `// JavaScript: 灵活的函数定义
// 函数声明
function add(a, b) {
  return a + b;
}

// 函数表达式
const multiply = function(a, b) {
  return a * b;
};

// 箭头函数
const subtract = (a, b) => a - b;

// 默认参数
function greet(name = "World", greeting = "Hello") {
  return \`\${greeting}, \${name}!\`;
}

// 解构参数
function createUser({ name, age, email }) {
  return { name, age, email };
}

const user = createUser({
  name: "Alice",
  age: 30,
  email: "alice@example.com"
});

// 剩余参数
function sum(...numbers) {
  return numbers.reduce((acc, n) => acc + n, 0);
}

console.log(sum(1, 2, 3, 4, 5)); // 15`,
    rightCode: `// Java: 严格的方法定义
public class Main {
  // 方法重载 - 相同方法名，不同参数
  public static int add(int a, int b) {
    return a + b;
  }

  public static double add(double a, double b) {
    return a + b;
  }

  // 可变参数
  public static int sum(int... numbers) {
    int total = 0;
    for (int num : numbers) {
      total += num;
    }
    return total;
  }

  // 使用类封装参数（类似解构）
  static class User {
    String name;
    int age;
    String email;

    User(String name, int age, String email) {
      this.name = name;
      this.age = age;
      this.email = email;
    }
  }

  public static User createUser(User user) {
    return user;
  }

  // 使用 Builder 模式（更灵活的参数传递）
  static class UserBuilder {
    private String name;
    private int age;
    private String email;

    public UserBuilder setName(String name) {
      this.name = name;
      return this;
    }

    public UserBuilder setAge(int age) {
      this.age = age;
      return this;
    }

    public UserBuilder setEmail(String email) {
      this.email = email;
      return this;
    }

    public User build() {
      return new User(name, age, email);
    }
  }

  public static void main(String[] args) {
    System.out.println(add(5, 3)); // 8
    System.out.println(add(5.5, 3.2)); // 8.7
    System.out.println(sum(1, 2, 3, 4, 5)); // 15

    User user = new UserBuilder()
      .setName("Alice")
      .setAge(30)
      .setEmail("alice@example.com")
      .build();
  }
}`,
    leftLanguage: 'javascript',
    rightLanguage: 'java',
    titleLeft: 'JavaScript',
    titleRight: 'Java',
    description: '函数和方法定义对比 - 灵活性 vs 严格性',
    tags: ['函数', '方法', '参数', '重载'],
    difficulty: 'intermediate',
    category: '函数编程'
  }
};