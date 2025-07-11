// English code examples configuration
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
    leftCode: `// JavaScript recursive function
function factorial(n) {
  if (n === 0) return 1;
  return n * factorial(n - 1);
}

// Usage example
console.log(factorial(5)); // 120

// Arrow function version
const factorialArrow = (n) => n === 0 ? 1 : n * factorialArrow(n - 1);
console.log(factorialArrow(5)); // 120

// Tail recursion optimization version
function factorialTail(n, acc = 1) {
  if (n === 0) return acc;
  return factorialTail(n - 1, n * acc);
}
console.log(factorialTail(5)); // 120`,
    rightCode: `# Python recursive function
def factorial(n):
    if n == 0:
        return 1
    return n * factorial(n - 1)

# Usage example
print(factorial(5))  # 120

# Lambda function version
factorial_lambda = lambda n: 1 if n == 0 else n * factorial_lambda(n - 1)
print(factorial_lambda(5))  # 120

# Tail recursion optimization version
def factorial_tail(n, acc=1):
    if n == 0:
        return acc
    return factorial_tail(n - 1, n * acc)
print(factorial_tail(5))  # 120`,
    leftLanguage: 'javascript',
    rightLanguage: 'python',
    titleLeft: 'JavaScript',
    titleRight: 'Python',
    description: 'Recursive function syntax comparison - from basic to optimized',
    tags: ['Functions', 'Recursion', 'Basic Syntax', 'Optimization'],
    difficulty: 'beginner',
    category: 'Functional Programming'
  },
  'js-rust': {
    leftCode: `// JavaScript functions and type system
function sum(a, b) {
  return a + b;
}

// Usage example
console.log(sum(5, 3)); // 8

// Arrow function
const multiply = (a, b) => a * b;
console.log(multiply(4, 6)); // 24

// Object destructuring
const person = { name: 'Alice', age: 30 };
const { name, age } = person;
console.log(name, age);

// Array destructuring
const [first, second, ...rest] = [1, 2, 3, 4, 5];
console.log(first, second, rest);

// Default parameters
function greet(name = 'World', greeting = 'Hello') {
  return \`\${greeting}, \${name}!\`;
}
console.log(greet()); // "Hello, World!"
console.log(greet('Alice', 'Hi')); // "Hi, Alice!"`,
    rightCode: `// Rust functions and type system
fn sum(a: i32, b: i32) -> i32 {
    a + b
}

// Usage example
fn main() {
    println!("{}", sum(5, 3)); // 8
    
    // Closure
    let multiply = |a: i32, b: i32| a * b;
    println!("{}", multiply(4, 6)); // 24
    
    // Struct destructuring
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
    
    // Array destructuring
    let arr = [1, 2, 3, 4, 5];
    let [first, second, ..rest] = arr;
    println!("{} {} {:?}", first, second, rest);
    
    // Default parameters via Option
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
    description: 'Function definition and type system comparison - dynamic vs static typing',
    tags: ['Functions', 'Types', 'Destructuring', 'Default Parameters'],
    difficulty: 'intermediate',
    category: 'Functional Programming'
  },
  'py-js': {
    leftCode: `# Python list comprehensions and functional programming
numbers = [1, 2, 3, 4, 5, 6, 7, 8, 9, 10]

# List comprehension
squares = [x**2 for x in numbers if x % 2 == 0]
print(squares)  # [4, 16, 36, 64, 100]

# Dictionary comprehension
square_dict = {x: x**2 for x in numbers if x % 2 == 0}
print(square_dict)  # {2: 4, 4: 16, 6: 36, 8: 64, 10: 100}

# Generator expression
squares_gen = (x**2 for x in numbers if x % 2 == 0)
print(list(squares_gen))  # [4, 16, 36, 64, 100]

# Functional programming
from functools import reduce
from operator import mul

# Map and filter
even_squares = list(map(lambda x: x**2, filter(lambda x: x % 2 == 0, numbers)))
print(even_squares)  # [4, 16, 36, 64, 100]

# Reduce
product = reduce(mul, numbers, 1)
print(product)  # 3628800`,
    rightCode: `// JavaScript array methods and functional programming
const numbers = [1, 2, 3, 4, 5, 6, 7, 8, 9, 10];

// Array methods (equivalent to list comprehension)
const squares = numbers.filter(x => x % 2 === 0).map(x => x ** 2);
console.log(squares); // [4, 16, 36, 64, 100]

// Object creation (equivalent to dict comprehension)
const squareDict = Object.fromEntries(
  numbers.filter(x => x % 2 === 0).map(x => [x, x ** 2])
);
console.log(squareDict); // {2: 4, 4: 16, 6: 36, 8: 64, 10: 100}

// Generator function (equivalent to generator expression)
function* squareGenerator(arr) {
  for (const x of arr) {
    if (x % 2 === 0) {
      yield x ** 2;
    }
  }
}
const squaresGen = Array.from(squareGenerator(numbers));
console.log(squaresGen); // [4, 16, 36, 64, 100]

// Functional programming with reduce
const evenSquares = numbers
  .filter(x => x % 2 === 0)
  .map(x => x ** 2);
console.log(evenSquares); // [4, 16, 36, 64, 100]

// Reduce for product
const product = numbers.reduce((acc, x) => acc * x, 1);
console.log(product); // 3628800`,
    leftLanguage: 'python',
    rightLanguage: 'javascript',
    titleLeft: 'Python',
    titleRight: 'JavaScript',
    description: 'List comprehensions vs array methods - functional programming patterns',
    tags: ['Arrays', 'Functional Programming', 'Comprehensions', 'Generators'],
    difficulty: 'intermediate',
    category: 'Data Structures'
  },
  'py-rust': {
    leftCode: `# Python classes and object-oriented programming
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

# Inheritance
class Student(Person):
    def __init__(self, name: str, age: int, student_id: str):
        super().__init__(name, age)
        self.student_id = student_id
    
    def greet(self) -> str:
        return f"Hi, I'm {self.name}, a student with ID {self.student_id}"

# Usage
person = Person("Alice", 25)
print(person.greet())  # Hello, my name is Alice and I'm 25 years old

student = Student("Bob", 20, "S12345")
print(student.greet())  # Hi, I'm Bob, a student with ID S12345`,
    rightCode: `// Rust structs and object-oriented patterns
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

// Implement Display trait for string representation
impl std::fmt::Display for Person {
    fn fmt(&self, f: &mut std::fmt::Formatter) -> std::fmt::Result {
        write!(f, "Person(name='{}', age={})", self.name, self.age)
    }
}

// Inheritance via composition
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

// Usage
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
    description: 'Object-oriented programming patterns - classes vs structs and traits',
    tags: ['OOP', 'Classes', 'Inheritance', 'Structs', 'Traits'],
    difficulty: 'intermediate',
    category: 'Object-Oriented Programming'
  },
  'rust-js': {
    leftCode: `// Rust error handling and Result type
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
    // Error handling with match
    match read_file_content("data.txt") {
        Ok(content) => {
            match process_data(&content) {
                Ok(numbers) => println!("Processed numbers: {:?}", numbers),
                Err(e) => eprintln!("Processing error: {}", e),
            }
        }
        Err(e) => eprintln!("File error: {}", e),
    }
    
    // Using ? operator for cleaner error handling
    fn process_file(filename: &str) -> Result<Vec<i32>, Box<dyn std::error::Error>> {
        let content = read_file_content(filename)?;
        let numbers = process_data(&content)?;
        Ok(numbers)
    }
}`,
    rightCode: `// JavaScript error handling and async/await
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
    // Error handling with try-catch
    const content = await readFileContent('data.txt');
    const numbers = processData(content);
    console.log('Processed numbers:', numbers);
  } catch (error) {
    console.error('Error:', error.message);
  }
}

// Using async/await for cleaner error handling
async function processFile(filename) {
  try {
    const content = await readFileContent(filename);
    return processData(content);
  } catch (error) {
    throw new Error(\`File processing failed: \${error.message}\`);
  }
}

// Call the main function
main();`,
    leftLanguage: 'rust',
    rightLanguage: 'javascript',
    titleLeft: 'Rust',
    titleRight: 'JavaScript',
    description: 'Error handling patterns - Result type vs try-catch',
    tags: ['Error Handling', 'Result Type', 'Async/Await', 'File I/O'],
    difficulty: 'advanced',
    category: 'Error Handling'
  },
  'js-cpp': {
    leftCode: `// JavaScript: Dynamic Typing
// The same function can accept different types of arguments
function add(a, b) {
  return a + b;
}

// 1. Used for number addition
console.log('5 + 10 =', add(5, 10));

// 2. Used for string concatenation
console.log('"Hello, " + "World!" =', add('Hello, ', 'World!'));`,
    rightCode: `#include <iostream>
#include <string>

// C++: Static Typing and Templates
// Use templates to generate specialized functions for different types
template <typename T>
T add(T a, T b) {
  return a + b;
}

int main() {
  // 1. Used for integer addition (T is deduced as int)
  std::cout << "5 + 10 = " << add(5, 10) << std::endl;

  // 2. Used for string concatenation (T is deduced as std::string)
  std::cout << "\"Hello, \" + \"World!\" = " 
            << add(std::string("Hello, "), std::string("World!")) 
            << std::endl;
            
  return 0;
}`,
    leftLanguage: 'javascript',
    rightLanguage: 'cpp',
    titleLeft: 'JavaScript',
    titleRight: 'C++',
    description: 'A comparison of function definitions and type systems, highlighting JavaScript\'s dynamic typing versus C++\'s static typing and templates.',
    tags: ['Type System', 'Functions', 'Templates', 'Dynamic vs Static'],
    difficulty: 'beginner',
    category: 'Core Concepts'
  }
};