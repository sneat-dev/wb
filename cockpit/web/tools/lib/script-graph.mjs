// The JavaScript an entry document loads, measured the one way every budget uses (cockpit-views#req:initial-script-size):
// the scripts the entry document loads at once with the chunks they import statically, and for a page the chunks it needs
// on top of them. Over JavaScript files only; styles, fonts and the lazy chunks a page loads later are not part of it.
import { existsSync, readFileSync, statSync } from 'node:fs'
import { join } from 'node:path'

const ENTRY_SCRIPTS = /<script\b[^>]*\bsrc="([^"]+)"/g
const PRELOADS = /<link\b[^>]*\brel="modulepreload"[^>]*\bhref="([^"]+)"/g
// A static import or re-export of a sibling chunk, in minified output; a dynamic
// `import("./x.js")` has a parenthesis and is not matched.
const STATIC_IMPORT = /(?:\bfrom|\bimport)\s*["']\.\/([^"']+)["']/g

const PLAIN_NAME = /^[A-Za-z0-9._-]+$/

// The JavaScript the entry document loads at once: its scripts and preloads and
// every chunk those import statically, as file names directly under dist. It fails
// closed: a script that is named but missing, or not a plain file name directly
// under dist, is a problem, so a build that moved or lost a file cannot pass.
export function initialScripts(dist) {
  const html = readFileSync(join(dist, 'index.html'), 'utf8')
  const problems = []
  const pending = [...html.matchAll(ENTRY_SCRIPTS), ...html.matchAll(PRELOADS)].map((match) => match[1])
  const found = new Set()
  while (pending.length > 0) {
    const name = pending.pop()
    if (found.has(name)) continue
    if (!PLAIN_NAME.test(name) || name === '.' || name === '..' || !existsSync(join(dist, name))) {
      problems.push(`${name} is named by the build but is not a file directly under dist`)
      continue
    }
    found.add(name)
    for (const match of readFileSync(join(dist, name), 'utf8').matchAll(STATIC_IMPORT)) pending.push(match[1])
  }
  if (found.size === 0) problems.push('dist/index.html loads no script')
  return { files: [...found].sort(), problems }
}

// Closes `names` under the static imports of the build's metafile (dist/stats.json).
function staticClosure(outputs, names) {
  const found = new Set()
  const pending = [...names]
  while (pending.length > 0) {
    const name = pending.pop()
    if (found.has(name) || outputs[name] === undefined) continue
    found.add(name)
    for (const edge of outputs[name].imports ?? []) if (edge.kind === 'import-statement') pending.push(edge.path)
  }
  return found
}

// The scripts needed to render the page whose source file ends with `entry`:
// the initial scripts, the page's own chunk with what it imports statically, and
// the lazy chunks that load it (the route's own). Needs the metafile the production build writes; without it, or
// when no output is that page, it fails closed.
export function firstPageScripts(dist, initial, entry) {
  const statsPath = join(dist, 'stats.json')
  if (!existsSync(statsPath)) return { files: [], problems: ['dist/stats.json is missing: build with "statsJson": true'] }
  const { outputs } = JSON.parse(readFileSync(statsPath, 'utf8'))
  const target = Object.keys(outputs).find((name) => outputs[name].entryPoint?.endsWith(entry))
  if (target === undefined) return { files: [], problems: [`no build output is ${entry}`] }
  const loaded = staticClosure(outputs, [target])
  for (let grew = true; grew; ) {
    grew = false
    for (const [name, output] of Object.entries(outputs)) {
      if (loaded.has(name) || initial.includes(name)) continue
      if ((output.imports ?? []).some((edge) => edge.kind === 'dynamic-import' && loaded.has(edge.path))) {
        for (const member of staticClosure(outputs, [name])) loaded.add(member)
        grew = true
      }
    }
  }
  const files = [...new Set([...initial, ...[...loaded].filter((name) => name.endsWith('.js'))])].sort()
  return { files, problems: [] }
}

export const kilobytes = (bytes) => `${(bytes / 1000).toFixed(2)} kB`
export const sizeOf = (dist, names) => names.map((name) => ({ name, bytes: statSync(join(dist, name)).size }))
export const total = (files) => files.reduce((sum, file) => sum + file.bytes, 0)
