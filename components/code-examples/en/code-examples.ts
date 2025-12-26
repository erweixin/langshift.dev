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
  },
  'js-go': {
    leftCode: `// JavaScript async programming: Promise and async/await
// Simulate async operation
function fetchData() {
  return new Promise((resolve) => {
    setTimeout(() => {
      resolve({ id: 1, name: 'Alice' });
    }, 1000);
  });
}

// Using async/await
async function getUser() {
  console.log('Fetching user data...');
  const user = await fetchData();
  console.log('User:', user);
  return user;
}

// Using Promise.then()
fetchData().then(user => {
  console.log('User:', user);
});

// Parallel requests
Promise.all([
  fetchData(),
  fetchData(),
]).then(users => {
  console.log('Multiple users:', users);
});`,
    rightCode: `// Go concurrent programming: Goroutines and Channels
package main

import (
  "fmt"
  "time"
)

// Simulate async operation
func fetchData(userChan chan<- map[string]interface{}) {
  time.Sleep(time.Second)
  userChan <- map[string]interface{}{
    "id":   1,
    "name": "Alice",
  }
}

// Using goroutine and channel
func getUser() {
  fmt.Println("Fetching user data...")
  userChan := make(chan map[string]interface{})
  go fetchData(userChan)
  user := <-userChan
  fmt.Println("User:", user)
}

// Using goroutine and channel
func main() {
  // Single request
  userChan := make(chan map[string]interface{})
  go fetchData(userChan)
  user := <-userChan
  fmt.Println("User:", user)

  // Parallel requests (similar to Promise.all)
  userChan1 := make(chan map[string]interface{})
  userChan2 := make(chan map[string]interface{})
  go fetchData(userChan1)
  go fetchData(userChan2)
  user1 := <-userChan1
  user2 := <-userChan2
  fmt.Println("Multiple users:", []map[string]interface{}{user1, user2})
}`,
    leftLanguage: 'javascript',
    rightLanguage: 'go',
    titleLeft: 'JavaScript',
    titleRight: 'Go',
    description: 'Async programming patterns comparison - Promise/async-await vs Goroutines/Channels',
    tags: ['Async', 'Concurrency', 'Promise', 'Goroutines'],
    difficulty: 'intermediate',
    category: 'Async Programming'
  },
  'js-swift': {
    leftCode: `// JavaScript arrays and higher-order functions
const numbers = [1, 2, 3, 4, 5];

// Map - transform array
const doubled = numbers.map(x => x * 2);
console.log(doubled); // [2, 4, 6, 8, 10]

// Filter - filter array
const evens = numbers.filter(x => x % 2 === 0);
console.log(evens); // [2, 4]

// Reduce - reduce array
const sum = numbers.reduce((acc, x) => acc + x, 0);
console.log(sum); // 15

// Chaining
const result = numbers
  .filter(x => x % 2 === 0)
  .map(x => x * 2)
  .reduce((acc, x) => acc + x, 0);
console.log(result); // 12

// Find - find element
const found = numbers.find(x => x > 3);
console.log(found); // 4`,
    rightCode: `// Swift arrays and higher-order functions
import Foundation

let numbers = [1, 2, 3, 4, 5]

// Map - transform array
let doubled = numbers.map { $0 * 2 }
print(doubled) // [2, 4, 6, 8, 10]

// Filter - filter array
let evens = numbers.filter { $0 % 2 == 0 }
print(evens) // [2, 4]

// Reduce - reduce array
let sum = numbers.reduce(0) { $0 + $1 }
print(sum) // 15

// Chaining
let result = numbers
  .filter { $0 % 2 == 0 }
  .map { $0 * 2 }
  .reduce(0) { $0 + $1 }
print(result) // 12

// Find - find element
let found = numbers.first { $0 > 3 }
print(found as Any) // Optional(4)

// CompactMap - handle optionals
let strings = ["1", "2", "abc", "4"]
let numbersFromStrings = strings.compactMap { Int($0) }
print(numbersFromStrings) // [1, 2, 4]`,
    leftLanguage: 'javascript',
    rightLanguage: 'swift',
    titleLeft: 'JavaScript',
    titleRight: 'Swift',
    description: 'Arrays and functional programming - higher-order functions and chaining',
    tags: ['Arrays', 'Functional Programming', 'Higher-Order Functions', 'Map/Filter/Reduce'],
    difficulty: 'beginner',
    category: 'Data Structures'
  },
  'js-c': {
    leftCode: `// JavaScript: Dynamic arrays and objects
// Arrays can grow dynamically
const arr = [1, 2, 3];
arr.push(4);
arr.push(5);
console.log(arr); // [1, 2, 3, 4, 5]

// Arrays can store any type
arr.push("hello");
arr.push({ name: "Alice" });
console.log(arr); // [1, 2, 3, 4, 5, "hello", { name: "Alice" }]

// Objects can add properties dynamically
const obj = {};
obj.name = "Alice";
obj.age = 30;
obj.greet = function() {
  return \`Hello, I'm \${this.name}\`;
};
console.log(obj.greet()); // "Hello, I'm Alice"

// Automatic memory management
let data = [];
for (let i = 0; i < 1000; i++) {
  data.push({ id: i, value: Math.random() });
}
// Garbage collector automatically cleans memory`,
    rightCode: `#include <stdio.h>
#include <stdlib.h>
#include <string.h>

// C: Manual memory management and fixed-size arrays
// Array size must be fixed
int arr[5] = {1, 2, 3, 4, 5};

// Dynamic arrays require manual memory management
int* create_dynamic_array(int size) {
  int* arr = (int*)malloc(size * sizeof(int));
  if (arr == NULL) {
    fprintf(stderr, "Memory allocation failed\\n");
    exit(1);
  }
  return arr;
}

// Struct definition
struct Person {
  char name[50];
  int age;
};

// Struct array
struct Person* create_person_array(int size) {
  struct Person* people = (struct Person*)malloc(size * sizeof(struct Person));
  if (people == NULL) {
    fprintf(stderr, "Memory allocation failed\\n");
    exit(1);
  }
  return people;
}

int main() {
  // Dynamic array example
  int size = 1000;
  int* data = create_dynamic_array(size);

  for (int i = 0; i < size; i++) {
    data[i] = i;
  }

  // Must manually free memory after use
  free(data);

  // Struct array example
  struct Person* people = create_person_array(10);
  strcpy(people[0].name, "Alice");
  people[0].age = 30;

  printf("Name: %s, Age: %d\\n", people[0].name, people[0].age);

  // Manually free memory
  free(people);

  return 0;
}`,
    leftLanguage: 'javascript',
    rightLanguage: 'c',
    titleLeft: 'JavaScript',
    titleRight: 'C',
    description: 'Memory management comparison - garbage collection vs manual memory management',
    tags: ['Memory Management', 'Arrays', 'Dynamic vs Static', 'Pointers'],
    difficulty: 'advanced',
    category: 'Memory Management'
  },
  'js-kt': {
    leftCode: `// JavaScript classes and object-oriented programming
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

// Inheritance
class Student extends Person {
  constructor(name, age, studentId) {
    super(name, age);
    this.studentId = studentId;
  }

  greet() {
    return \`Hi, I'm \${this.name} (ID: \${this.studentId})\`;
  }
}

// Usage
const person = new Person("Alice", 25);
console.log(person.greet()); // "Hello, I'm Alice"

const student = new Student("Bob", 20, "S12345");
console.log(student.greet()); // "Hi, I'm Bob (ID: S12345)"`,
    rightCode: `// Kotlin classes and object-oriented programming
// Data class - auto-generates equals, hashCode, toString, copy
data class Person(
  val name: String,
  val age: Int
) {
  fun greet() = "Hello, I'm $name"

  fun introduce() = "I'm $age years old"
}

// Sealed class - restricted class hierarchy
sealed class StudentStatus {
  data class Active(val gpa: Double) : StudentStatus()
  data class OnProbation(val reason: String) : StudentStatus()
  object Graduated : StudentStatus()
}

// Inheritance
open class Animal(val name: String) {
  open fun makeSound() = "Some sound"
}

class Dog(name: String) : Animal(name) {
  override fun makeSound() = "Woof!"
}

// Usage
fun main() {
  val person = Person("Alice", 25)
  println(person.greet()) // "Hello, I'm Alice"

  // Data class copy feature
  val olderPerson = person.copy(age = 26)
  println(olderPerson) // "Person(name=Alice, age=26)"

  val dog = Dog("Buddy")
  println(dog.makeSound()) // "Woof!"

  // Smart type casting
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
    description: 'Object-oriented programming comparison - class inheritance vs data classes and sealed classes',
    tags: ['OOP', 'Classes', 'Inheritance', 'Data Classes'],
    difficulty: 'intermediate',
    category: 'Object-Oriented Programming'
  },
  'js-java': {
    leftCode: `// JavaScript: Flexible function definitions
// Function declaration
function add(a, b) {
  return a + b;
}

// Function expression
const multiply = function(a, b) {
  return a * b;
};

// Arrow function
const subtract = (a, b) => a - b;

// Default parameters
function greet(name = "World", greeting = "Hello") {
  return \`\${greeting}, \${name}!\`;
}

// Destructuring parameters
function createUser({ name, age, email }) {
  return { name, age, email };
}

const user = createUser({
  name: "Alice",
  age: 30,
  email: "alice@example.com"
});

// Rest parameters
function sum(...numbers) {
  return numbers.reduce((acc, n) => acc + n, 0);
}

console.log(sum(1, 2, 3, 4, 5)); // 15`,
    rightCode: `// Java: Strict method definitions
public class Main {
  // Method overloading - same method name, different parameters
  public static int add(int a, int b) {
    return a + b;
  }

  public static double add(double a, double b) {
    return a + b;
  }

  // Varargs
  public static int sum(int... numbers) {
    int total = 0;
    for (int num : numbers) {
      total += num;
    }
    return total;
  }

  // Using class to encapsulate parameters (similar to destructuring)
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

  // Using Builder pattern (more flexible parameter passing)
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
    description: 'Function and method definition comparison - flexibility vs strictness',
    tags: ['Functions', 'Methods', 'Parameters', 'Overloading'],
    difficulty: 'intermediate',
    category: 'Functional Programming'
  }
};