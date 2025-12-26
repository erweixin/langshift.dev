# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 🎯 CRITICAL: Code Component Usage Priority

### ⭐ UniversalEditor Component (HIGHEST PRIORITY)

**ALWAYS use the `<UniversalEditor>` component for ALL code comparisons in learning module content.**

The UniversalEditor component provides:
- Interactive code editing in the browser
- Side-by-side language comparison
- Real-time code execution (where supported)
- Consistent user experience across all modules

#### When to Use UniversalEditor:
✅ **ALWAYS USE** for:
- Side-by-side language comparisons
- Interactive code examples
- Code pattern demonstrations
- Before/after code transformations
- Language feature comparisons

❌ **NEVER USE** (unless specifically required):
- Static code blocks without comparison
- Single-language examples without comparison context
- Non-interactive code displays

#### UniversalEditor Syntax:
```md
<UniversalEditor title="Descriptive Title">
```language !! syntax-highlighter
// Your code here
```

```other-language !! other-syntax-highlighter
// Your comparison code here
```
</UniversalEditor>
```

#### Example:
```md
<UniversalEditor title="Variable Declaration Comparison">
```python !! py
# Python - Dynamic typing
name = "Alice"
age = 25
```

```rust !! rust
// Rust - Static typing
let name: &str = "Alice";
let age: i32 = 25;
```
</UniversalEditor>
```

**IMPORTANT: UniversalEditor is the DEFAULT and PREFERRED component for all code examples in learning modules.**

### Other Code Components (Use Only When Appropriate):

#### `<Code>` Component
For simple syntax highlighting without interactivity:
```tsx
import { Code } from '@/components/code'

<Code language="python">{`code here`}</Code>
```

Use cases:
- Documentation sections
- Configuration files
- Non-interactive code snippets

#### `<VirtualizedEditor>` Component
For single-language interactive editing:
```tsx
import { VirtualizedEditor } from '@/components/virtualized-editor'

<VirtualizedEditor
  language="python"
  value={code}
  onChange={(value) => setCode(value)}
  height={300}
/>
```

Use cases:
- Single-language code editors
- Exercise/practice areas
- When only one language needs to be shown

## Project Overview

LangShift.dev is a programming language conversion learning platform built with Next.js 15.5.9 and Fumadocs 15.6.1. It helps developers learn new programming languages through comparative learning, starting from their existing language knowledge.

**Core Philosophy:** Teach new languages by comparing them to a known language, showing syntax mappings, concept translations, and performance differences.

### Supported Language Conversions
- JavaScript → Python (13 modules) ✅
- JavaScript → Rust (14 modules) ✅
- JavaScript → Go (14 modules) ✅
- JavaScript → Kotlin (14 modules) ✅
- JavaScript → C++ (15 modules) ✅
- JavaScript → Swift (15 modules) ✅
- JavaScript → C (15 modules) ✅
- JavaScript → Java (20 modules) ✅
- Python → JavaScript ✅
- Python → Rust (planned)

## Development Commands

```bash
# Development server (runs on port 8000)
pnpm dev

# Production build
pnpm build

# Start production server
pnpm start

# Type checking
pnpm type-check

# Linting
pnpm lint

# SEO checking (builds and validates sitemap/robots.txt)
pnpm seo-check

# Bundle analysis
pnpm analyze
```

**Important:** Always use `pnpm` as the package manager. The project uses Turbopack for faster development builds.

## Architecture Overview

### Tech Stack
- **Framework:** Next.js 15.5.9 with App Router
- **Documentation:** Fumadocs 15.6.1 + MDX
- **Styling:** Tailwind CSS 4.0.9
- **Editor:** Monaco Editor 4.7.0
- **Type Safety:** TypeScript 5.8.2 (strict mode)
- **Search:** Orama 3.1.1 full-text search
- **i18n:** Support for English, Simplified Chinese, Traditional Chinese

### Content Creation Guidelines

#### 1. Module Structure

Each language conversion path follows this structure:
```
content/docs/{source}2{target}/
├── meta.json           # Module metadata (keep simple!)
├── index.mdx           # English introduction
├── index.zh-cn.mdx     # Simplified Chinese introduction
├── index.zh-tw.mdx     # Traditional Chinese introduction
├── module-00-{topic}.mdx
├── module-00-{topic}.zh-cn.mdx
├── module-00-{topic}.zh-tw.mdx
└── ... (more modules)
```

#### 2. meta.json Format

**KEEP IT SIMPLE** - Only include title and root:
```json
{
  "title": "Python → Rust",
  "root": true
}
```

Do NOT add extra fields like pages, language, modules, etc.

#### 3. Language Versioning

**File naming convention:**
- Main file: `module-xx-topic.mdx` (English)
- Chinese simplified: `module-xx-topic.zh-cn.mdx`
- Chinese traditional: `module-xx-topic.zh-tw.mdx`

**CRITICAL:** The main file MUST be in English. Chinese versions use the appropriate suffix.

#### 4. Module Content Guidelines

**When creating learning content:**

1. **Always use UniversalEditor for code comparisons**
   - This is the highest priority rule
   - Every code comparison should use UniversalEditor
   - Provide meaningful titles for each comparison

2. **Start with the source language**
   - Explain new concepts by comparing them to the source language
   - Show "before" (source) and "after" (target) code
   - Highlight key differences

3. **Provide runnable code examples**
   - Code should be syntactically correct
   - Include comments explaining key concepts
   - Show realistic, production-relevant examples

4. **Include performance analysis**
   - Compare performance characteristics
   - Explain memory management differences
   - Discuss optimization strategies

5. **Show common pitfalls**
   - Highlight mistakes developers make when transitioning
   - Provide solutions and best practices
   - Include "gotcha" warnings

6. **Use progressive difficulty**
   - Start with basic concepts
   - Build complexity gradually
   - Each module should build on previous knowledge

#### 5. MDX Frontmatter

Each module file must have frontmatter:
```md
---
title: "Module XX: Topic Name"
description: "Brief description of what this module covers"
---
```

#### 6. UniversalEditor Best Practices

**DO:**
- Use descriptive titles that explain the comparison
- Include helpful comments in code
- Show realistic, practical examples
- Keep code snippets focused and concise
- Demonstrate idiomatic code in both languages

**DON'T:**
- Use overly simplistic "hello world" examples (unless it's module 0)
- Show contrived or unrealistic code
- Make code snippets too long (>50 lines preferred)
- Skip error handling or edge cases

#### 7. Module Planning

When creating a new language conversion path:

1. **Analyze the source and target languages**
   - Identify key conceptual differences
   - Map syntax patterns between languages
   - Note unique features of each language

2. **Plan 15-20 modules covering:**
   - Module 0: Introduction and environment setup
   - Modules 1-5: Basic syntax and concepts
   - Modules 6-10: Intermediate features and OOP
   - Modules 11-15: Advanced features and ecosystem
   - Modules 16-20: Best practices and real-world projects

3. **Create consistent structure**
   - Each module should follow a similar pattern
   - Include learning objectives at the start
   - Provide exercises at the end
   - Add summary sections

## File Naming Conventions

- **Learning modules:** `module-{number:02d}-{topic}.mdx` (e.g., `module-01-basics.mdx`)
- **Language conversion directories:** `{source}2{target}` (e.g., `py2rust`)
- **Components:** PascalCase (e.g., `VirtualizedEditor.tsx`)
- **Utilities:** camelCase (e.g., `monaco-manager.tsx`)

## Header Navigation

When adding a new language conversion path, update `components/header.tsx`:

```typescript
const SOURCE_LANGUAGES = [
  {
    id: 'python',
    name: 'Python',
    icon: '🐍',
    gradient: 'from-green-500 to-emerald-500',
    targets: [
      {
        id: 'rust',
        name: 'Rust',
        icon: '🦀',
        gradient: 'from-orange-500 to-red-500',
        path: 'py2rust',
        status: 'completed' as const,
      }
    ]
  }
]
```

## Common Patterns

### Component Structure
```tsx
'use client'  // Required for interactive components

import { useState, useEffect } from 'react'
import { useMonacoManager } from '@/components/monaco-manager'

export function MyComponent() {
  const { isReady, isLoading } = useMonacoManager()
  // Component logic
}
```

### Error Handling
- Always handle network errors for API calls
- Provide user-friendly error messages
- Log errors to console for debugging
- Implement retry logic where appropriate

### Internationalization
- Use `useTranslations` from Fumadocs UI
- Add translation keys to `/messages/*.json`
- Test all three languages (en, zh-cn, zh-tw)
- Ensure language detection works correctly

## Testing Before Deployment

1. **Type checking:** `pnpm type-check` must pass
2. **Linting:** `pnpm lint` must pass
3. **Build:** `pnpm build` must succeed
4. **SEO:** `pnpm seo-check` validates sitemap and robots.txt
5. **Manual testing:** Test UniversalEditor components for each language
6. **Performance:** Run `pnpm analyze` to check bundle size

## Summary of Key Rules

1. ⭐ **ALWAYS use UniversalEditor for code comparisons** (Highest Priority)
2. Main module files MUST be in English
3. Keep meta.json simple (only title and root)
4. Use consistent file naming conventions
5. Provide practical, realistic code examples
6. Include progressive difficulty levels
7. Test all three language versions
