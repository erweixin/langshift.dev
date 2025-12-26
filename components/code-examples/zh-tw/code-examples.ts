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
  },
  'js-go': {
    leftCode: `// JavaScript 異步編程：Promise 和 async/await
// 模擬異步操作
function fetchData() {
  return new Promise((resolve) => {
    setTimeout(() => {
      resolve({ id: 1, name: 'Alice' });
    }, 1000);
  });
}

// 使用 async/await
async function getUser() {
  console.log('獲取用戶數據...');
  const user = await fetchData();
  console.log('用戶:', user);
  return user;
}

// 使用 Promise.then()
fetchData().then(user => {
  console.log('用戶:', user);
});

// 並發請求
Promise.all([
  fetchData(),
  fetchData(),
]).then(users => {
  console.log('多個用戶:', users);
});`,
    rightCode: `// Go 並發編程：Goroutines 和 Channels
package main

import (
  "fmt"
  "time"
)

// 模擬異步操作
func fetchData(userChan chan<- map[string]interface{}) {
  time.Sleep(time.Second)
  userChan <- map[string]interface{}{
    "id":   1,
    "name": "Alice",
  }
}

// 使用 goroutine 和 channel
func getUser() {
  fmt.Println("獲取用戶數據...")
  userChan := make(chan map[string]interface{})
  go fetchData(userChan)
  user := <-userChan
  fmt.Println("用戶:", user)
}

// 使用 goroutine 和 channel
func main() {
  // 單個請求
  userChan := make(chan map[string]interface{})
  go fetchData(userChan)
  user := <-userChan
  fmt.Println("用戶:", user)

  // 並發請求（類似 Promise.all）
  userChan1 := make(chan map[string]interface{})
  userChan2 := make(chan map[string]interface{})
  go fetchData(userChan1)
  go fetchData(userChan2)
  user1 := <-userChan1
  user2 := <-userChan2
  fmt.Println("多個用戶:", []map[string]interface{}{user1, user2})
}`,
    leftLanguage: 'javascript',
    rightLanguage: 'go',
    titleLeft: 'JavaScript',
    titleRight: 'Go',
    description: '異步編程模式對比 - Promise/async-await vs Goroutines/Channels',
    tags: ['異步編程', '並發', 'Promise', 'Goroutines'],
    difficulty: 'intermediate',
    category: '異步編程'
  },
  'js-swift': {
    leftCode: `// JavaScript 數組和高階函數
const numbers = [1, 2, 3, 4, 5];

// Map - 轉換數組
const doubled = numbers.map(x => x * 2);
console.log(doubled); // [2, 4, 6, 8, 10]

// Filter - 過濾數組
const evens = numbers.filter(x => x % 2 === 0);
console.log(evens); // [2, 4]

// Reduce - 歸約數組
const sum = numbers.reduce((acc, x) => acc + x, 0);
console.log(sum); // 15

// 鏈式調用
const result = numbers
  .filter(x => x % 2 === 0)
  .map(x => x * 2)
  .reduce((acc, x) => acc + x, 0);
console.log(result); // 12

// Find - 查找元素
const found = numbers.find(x => x > 3);
console.log(found); // 4`,
    rightCode: `// Swift 數組和高階函數
import Foundation

let numbers = [1, 2, 3, 4, 5]

// Map - 轉換數組
let doubled = numbers.map { $0 * 2 }
print(doubled) // [2, 4, 6, 8, 10]

// Filter - 過濾數組
let evens = numbers.filter { $0 % 2 == 0 }
print(evens) // [2, 4]

// Reduce - 歸約數組
let sum = numbers.reduce(0) { $0 + $1 }
print(sum) // 15

// 鏈式調用
let result = numbers
  .filter { $0 % 2 == 0 }
  .map { $0 * 2 }
  .reduce(0) { $0 + $1 }
print(result) // 12

// Find - 查找元素
let found = numbers.first { $0 > 3 }
print(found as Any) // Optional(4)

// CompactMap - 處理可選值
let strings = ["1", "2", "abc", "4"]
let numbersFromStrings = strings.compactMap { Int($0) }
print(numbersFromStrings) // [1, 2, 4]`,
    leftLanguage: 'javascript',
    rightLanguage: 'swift',
    titleLeft: 'JavaScript',
    titleRight: 'Swift',
    description: '數組和函數式編程 - 高階函數和鏈式調用',
    tags: ['數組', '函數式編程', '高階函數', 'Map/Filter/Reduce'],
    difficulty: 'beginner',
    category: '數據結構'
  },
  'js-c': {
    leftCode: `// JavaScript: 動態數組和對象
// 數組可以動態增長
const arr = [1, 2, 3];
arr.push(4);
arr.push(5);
console.log(arr); // [1, 2, 3, 4, 5]

// 數組可以存儲任意類型
arr.push("hello");
arr.push({ name: "Alice" });
console.log(arr); // [1, 2, 3, 4, 5, "hello", { name: "Alice" }]

// 對象動態添加屬性
const obj = {};
obj.name = "Alice";
obj.age = 30;
obj.greet = function() {
  return \`Hello, I'm \${this.name}\`;
};
console.log(obj.greet()); // "Hello, I'm Alice"

// 自動記憶體管理
let data = [];
for (let i = 0; i < 1000; i++) {
  data.push({ id: i, value: Math.random() });
}
// 垃圾回收器會自動清理記憶體`,
    rightCode: `#include <stdio.h>
#include <stdlib.h>
#include <string.h>

// C: 手動記憶體管理和固定大小數組
// 數組大小必須固定
int arr[5] = {1, 2, 3, 4, 5};

// 動態數組需要手動管理記憶體
int* create_dynamic_array(int size) {
  int* arr = (int*)malloc(size * sizeof(int));
  if (arr == NULL) {
    fprintf(stderr, "記憶體分配失敗\\n");
    exit(1);
  }
  return arr;
}

// 結構體定義
struct Person {
  char name[50];
  int age;
};

// 結構體數組
struct Person* create_person_array(int size) {
  struct Person* people = (struct Person*)malloc(size * sizeof(struct Person));
  if (people == NULL) {
    fprintf(stderr, "記憶體分配失敗\\n");
    exit(1);
  }
  return people;
}

int main() {
  // 動態數組示例
  int size = 1000;
  int* data = create_dynamic_array(size);

  for (int i = 0; i < size; i++) {
    data[i] = i;
  }

  // 使用完必須手動釋放記憶體
  free(data);

  // 結構體數組示例
  struct Person* people = create_person_array(10);
  strcpy(people[0].name, "Alice");
  people[0].age = 30;

  printf("Name: %s, Age: %d\\n", people[0].name, people[0].age);

  // 手動釋放記憶體
  free(people);

  return 0;
}`,
    leftLanguage: 'javascript',
    rightLanguage: 'c',
    titleLeft: 'JavaScript',
    titleRight: 'C',
    description: '記憶體管理對比 - 垃圾回收 vs 手動記憶體管理',
    tags: ['記憶體管理', '數組', '動態 vs 靜態', '指針'],
    difficulty: 'advanced',
    category: '記憶體管理'
  },
  'js-kt': {
    leftCode: `// JavaScript 類和面向對象編程
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

// 繼承
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
    rightCode: `// Kotlin 類和面向對象編程
// 數據類 - 自動生成 equals, hashCode, toString, copy
data class Person(
  val name: String,
  val age: Int
) {
  fun greet() = "Hello, I'm $name"

  fun introduce() = "I'm $age years old"
}

// 密封類 - 受限的類繼承結構
sealed class StudentStatus {
  data class Active(val gpa: Double) : StudentStatus()
  data class OnProbation(val reason: String) : StudentStatus()
  object Graduated : StudentStatus()
}

// 繼承
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

  // 數據類的 copy 功能
  val olderPerson = person.copy(age = 26)
  println(olderPerson) // "Person(name=Alice, age=26)"

  val dog = Dog("Buddy")
  println(dog.makeSound()) // "Woof!"

  // 智能類型轉換
  fun checkStatus(status: StudentStatus) = when (status) {
    is StudentStatus.Active -> "Active with GPA ${status.gpa}"
    is StudentStatus.OnProbation -> "On probation: ${status.reason}"
    is StudentStatus.Graduated -> "Graduated"
  }
}`,
    leftLanguage: 'javascript',
    rightLanguage: 'kotlin',
    titleLeft: 'JavaScript',
    titleRight: 'Kotlin',
    description: '面向對象編程對比 - 類繼承 vs 數據類和密封類',
    tags: ['面向對象', '類', '繼承', '數據類'],
    difficulty: 'intermediate',
    category: '面向對象編程'
  },
  'js-java': {
    leftCode: `// JavaScript: 靈活的函數定義
// 函數聲明
function add(a, b) {
  return a + b;
}

// 函數表達式
const multiply = function(a, b) {
  return a * b;
};

// 箭頭函數
const subtract = (a, b) => a - b;

// 默認參數
function greet(name = "World", greeting = "Hello") {
  return \`\${greeting}, \${name}!\`;
}

// 解構參數
function createUser({ name, age, email }) {
  return { name, age, email };
}

const user = createUser({
  name: "Alice",
  age: 30,
  email: "alice@example.com"
});

// 剩餘參數
function sum(...numbers) {
  return numbers.reduce((acc, n) => acc + n, 0);
}

console.log(sum(1, 2, 3, 4, 5)); // 15`,
    rightCode: `// Java: 嚴格的方法定義
public class Main {
  // 方法重載 - 相同方法名，不同參數
  public static int add(int a, int b) {
    return a + b;
  }

  public static double add(double a, double b) {
    return a + b;
  }

  // 可變參數
  public static int sum(int... numbers) {
    int total = 0;
    for (int num : numbers) {
      total += num;
    }
    return total;
  }

  // 使用類封裝參數（類似解構）
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

  // 使用 Builder 模式（更靈活的參數傳遞）
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
    description: '函數和方法定義對比 - 靈活性 vs 嚴格性',
    tags: ['函數', '方法', '參數', '重載'],
    difficulty: 'intermediate',
    category: '函數編程'
  }
};