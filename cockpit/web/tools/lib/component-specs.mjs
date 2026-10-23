// Fails when a component has no spec that renders it. Coverage tooling does not
// measure Angular templates, so a component can sit at 100% covered while its
// template never runs; this check closes that gap.
//
// A component is every class decorated with @Component in a non-spec .ts file
// under any src directory of apps/* or libs/**. Its spec is the sibling
// <name>.spec.ts, and it renders the component only when it calls
// createComponent(<ClassName> outside a comment and outside a skipped block
// (it.skip, test.skip, describe.skip, xit, xtest, xdescribe).
import { existsSync, readdirSync, readFileSync } from 'node:fs'
import { join, relative } from 'node:path'

export function stripComments(text) {
  return text.replace(/\/\*[\s\S]*?\*\//g, '').replace(/(^|[^:])\/\/.*$/gm, '$1')
}

// Index of the parenthesis closing the one at `open`, string-aware; the end of
// the text when it never closes.
function closingParen(text, open) {
  let depth = 0
  let quote = ''
  for (let i = open; i < text.length; i++) {
    const char = text[i]
    if (quote) {
      if (char === '\\') i++
      else if (char === quote) quote = ''
    } else if (char === "'" || char === '"' || char === '`') {
      quote = char
    } else if (char === '(') {
      depth++
    } else if (char === ')' && --depth === 0) {
      return i
    }
  }
  return text.length
}

export function withoutSkipped(text) {
  let result = text
  let match
  while ((match = /\b(?:(?:it|test|describe)\.skip|xit|xtest|xdescribe)\s*\(/.exec(result))) {
    const open = match.index + match[0].length - 1
    result = result.slice(0, match.index) + result.slice(closingParen(result, open) + 1)
  }
  return result
}

export function componentClasses(source) {
  const decorated =
    /@Component\s*\([\s\S]*?\)\s*(?:@\w+\([^)]*\)\s*)*(?:export\s+)?(?:default\s+)?(?:abstract\s+)?class\s+(\w+)/g
  return [...stripComments(source).matchAll(decorated)].map((match) => match[1])
}

export function rendersComponent(spec, className) {
  const live = withoutSkipped(stripComments(spec))
  return new RegExp(`createComponent\\(\\s*${className}\\b`).test(live)
}

function sourceFiles(directory) {
  const found = []
  for (const entry of readdirSync(directory, { withFileTypes: true })) {
    const path = join(directory, entry.name)
    if (entry.isDirectory()) {
      found.push(...sourceFiles(path))
    } else if (entry.name.endsWith('.ts') && !entry.name.endsWith('.spec.ts') && !entry.name.endsWith('.d.ts')) {
      found.push(path)
    }
  }
  return found
}

function sourceRoots(root) {
  const found = []
  const walk = (directory) => {
    for (const entry of readdirSync(directory, { withFileTypes: true })) {
      if (!entry.isDirectory() || entry.name === 'node_modules') continue
      const path = join(directory, entry.name)
      if (entry.name === 'src') found.push(path)
      else walk(path)
    }
  }
  for (const group of ['apps', 'libs']) {
    if (existsSync(join(root, group))) walk(join(root, group))
  }
  return found
}

function components(root) {
  const found = []
  for (const src of sourceRoots(root)) {
    for (const file of sourceFiles(src)) {
      for (const className of componentClasses(readFileSync(file, 'utf8'))) found.push({ file, className })
    }
  }
  return found
}

export function findUnrenderedComponents(root) {
  return components(root)
    .filter(({ file, className }) => {
      const spec = file.replace(/\.ts$/, '.spec.ts')
      return !existsSync(spec) || !rendersComponent(readFileSync(spec, 'utf8'), className)
    })
    .map(({ file, className }) => `${relative(root, file).split('\\').join('/')} (${className})`)
    .sort()
}

export function main(root, log) {
  const missing = findUnrenderedComponents(root)
  if (missing.length) {
    log('component-specs: these components have no live spec that renders them (createComponent):')
    for (const entry of missing) log(`  ${entry}`)
    return 1
  }
  const count = components(root).length
  log(`component-specs: ${count} component${count === 1 ? '' : 's'}, each rendered by its spec`)
  return 0
}
