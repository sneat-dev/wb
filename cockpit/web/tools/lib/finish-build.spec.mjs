import { existsSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { gunzipSync } from 'node:zlib'
import { readFileSync } from 'node:fs'
import { FIRST_PAGE_ENTRY, GALLERY_MARKERS, HOME_BUDGET, NONCE_PLACEHOLDER, ROUTE_BUDGET, finishBuild, galleryLeaks, firstPageScripts, initialScripts, precompress } from './finish-build.mjs'
import { routeBudgets } from './route-budgets.mjs'

let dist

// The routes source of a build that has only Home: the page route whose entry is FIRST_PAGE_ENTRY.
const ROUTES = "export const pageRoutes = [{ ...page('', 'Home', () => import('./pages/home/home-page').then((m) => m.HomePage)), pathMatch: 'full' }]"
const build = (log = () => {}, report = () => {}) => finishBuild(dist, log, report, ROUTES)

beforeEach(() => {
  dist = mkdtempSync(join(tmpdir(), 'finish-build-'))
})

afterEach(() => {
  rmSync(dist, { recursive: true, force: true })
})

// A built application: main imports a shared chunk statically (which re-exports a third) and a
// prime-theme chunk and the overlays dynamically; the prime-theme chunk loads the Home page
// chunk dynamically; Home imports a library chunk statically. `mainBytes` sizes main.
function writeApplication({ mainBytes = 1000, homeBytes = 500, primeBytes = 700, extraIndex = '', stats = true } = {}) {
  writeFileSync(
    join(dist, 'index.html'),
    `<link rel="stylesheet" href="styles-ABC.css"><app-root ngCspNonce="${NONCE_PLACEHOLDER}"></app-root><link rel="modulepreload" href="chunk-SHARED.js"><script src="main-ABC.js" type="module" nonce="${NONCE_PLACEHOLDER}"></script>${extraIndex}`,
  )
  writeFileSync(join(dist, 'main-ABC.js'), `import{a as b}from"./chunk-SHARED.js";import"./chunk-SIDE.js";const l=()=>import("./chunk-PRIME.js");const o=()=>import("./chunk-OVERLAYS.js");${'x'.repeat(mainBytes)}`)
  writeFileSync(join(dist, 'chunk-SHARED.js'), `export{c}from"./chunk-DEEP.js";${'y'.repeat(2000)}`)
  writeFileSync(join(dist, 'chunk-SIDE.js'), 'export{}')
  writeFileSync(join(dist, 'chunk-DEEP.js'), 'export const c=1')
  writeFileSync(join(dist, 'chunk-PRIME.js'), `const h=()=>import("./chunk-HOME.js");${'p'.repeat(primeBytes)}`)
  writeFileSync(join(dist, 'chunk-HOME.js'), `import"./chunk-LIB.js";${'h'.repeat(homeBytes)}`)
  writeFileSync(join(dist, 'chunk-LIB.js'), 'l'.repeat(300))
  writeFileSync(join(dist, 'chunk-OVERLAYS.js'), 'o'.repeat(900_000))
  writeFileSync(join(dist, 'styles-ABC.css'), 'a{color:red}'.repeat(100_000))
  if (stats) {
    const edge = (path, kind = 'import-statement') => ({ path, kind })
    writeFileSync(
      join(dist, 'stats.json'),
      JSON.stringify({
        outputs: {
          'main-ABC.js': { imports: [edge('chunk-SHARED.js'), edge('chunk-SIDE.js'), edge('chunk-DEEP.js'), edge('external-not-an-output.js'), edge('chunk-PRIME.js', 'dynamic-import'), edge('chunk-OVERLAYS.js', 'dynamic-import')] },
          'chunk-SHARED.js': { imports: [edge('chunk-DEEP.js')] },
          'chunk-SIDE.js': {},
          'chunk-DEEP.js': {},
          'chunk-PRIME.js': { entryPoint: 'apps/cockpit/src/app/pages/prime-theme.ts', imports: [edge('chunk-HOME.js', 'dynamic-import')] },
          'chunk-HOME.js': { entryPoint: `apps/cockpit/src/app/${FIRST_PAGE_ENTRY}`, imports: [edge('chunk-LIB.js'), edge('chunk-LIB.js'), edge('external-not-an-output.js')] },
          'chunk-LIB.js': {},
          'chunk-OVERLAYS.js': { entryPoint: 'apps/cockpit/src/app/shell/overlays.ts' },
        },
      }),
    )
  }
}

it('restores the placeholder file and succeeds for a good build', () => {
  writeApplication()
  const lines = []
  expect(build((line) => lines.push(line))).toBe(0)
  expect(existsSync(join(dist, '.gitkeep'))).toBe(true)
  expect(existsSync(join(dist, 'stats.json'))).toBe(false)
  expect(lines).toEqual([])
})

it('fails naming the entry document when the build emitted none', () => {
  mkdirSync(dist, { recursive: true })
  const lines = []
  expect(build((line) => lines.push(line))).toBe(1)
  expect(lines.join('')).toContain('dist/index.html')
})

it('fails naming the placeholder when the entry document lost it', () => {
  writeFileSync(join(dist, 'index.html'), '<app-root></app-root>')
  const lines = []
  expect(build((line) => lines.push(line))).toBe(1)
  expect(lines.join('')).toContain('__CSP_NONCE__')
  expect(existsSync(join(dist, '.gitkeep'))).toBe(false)
})

it('compresses every text asset beside the original and leaves the rest', () => {
  writeApplication()
  mkdirSync(join(dist, 'media'), { recursive: true })
  writeFileSync(join(dist, 'media', 'logo.svg'), '<svg>'.repeat(200))
  writeFileSync(join(dist, 'font.woff2'), 'binary'.repeat(200))
  writeFileSync(join(dist, 'main-ABC.js.map'), 'm'.repeat(500))
  mkdirSync(join(dist, 'folder.js'))
  expect(build()).toBe(0)
  expect(gunzipSync(readFileSync(join(dist, 'chunk-LIB.js.gz'))).toString()).toBe('l'.repeat(300))
  expect(existsSync(join(dist, 'media', 'logo.svg.gz'))).toBe(true)
  for (const skipped of ['index.html.gz', 'font.woff2.gz', 'main-ABC.js.map.gz', 'folder.js.gz']) {
    expect(existsSync(join(dist, skipped))).toBe(false)
  }
})

it('writes no compressed file that is not smaller than its original', () => {
  writeFileSync(join(dist, 'tiny.js'), 'x')
  expect(precompress(dist)).toBe(0)
  expect(existsSync(join(dist, 'tiny.js.gz'))).toBe(false)
})

it('counts the files it compressed', () => {
  writeFileSync(join(dist, 'a.css'), 'a{color:red}'.repeat(40))
  writeFileSync(join(dist, 'b.json'), JSON.stringify({ k: 'v'.repeat(300) }))
  expect(precompress(dist)).toBe(2)
})

// cockpit-views#ac:initial-script-fits-the-budget
it('counts the scripts the entry document loads, the preloads and what they import statically, and no lazy chunk or style', () => {
  writeApplication()
  expect(initialScripts(dist)).toEqual({ files: ['chunk-DEEP.js', 'chunk-SHARED.js', 'chunk-SIDE.js', 'main-ABC.js'], problems: [] })
})

it('counts for the first page the initial scripts, the page, what it imports and the lazy chunk that loads it, and not the overlays', () => {
  writeApplication()
  const initial = initialScripts(dist).files
  expect(firstPageScripts(dist, initial, FIRST_PAGE_ENTRY)).toEqual({
    files: ['chunk-DEEP.js', 'chunk-HOME.js', 'chunk-LIB.js', 'chunk-PRIME.js', 'chunk-SHARED.js', 'chunk-SIDE.js', 'main-ABC.js'],
    problems: [],
  })
  const budget = routeBudgets(dist, ROUTES)
  expect(budget.problems).toEqual([])
  expect(budget.initialBytes).toBeLessThan(budget.rows[0].bytes)
  expect(budget.rows[0].bytes).toBeLessThan(6000)
  expect(budget.rows[0].budget).toBe(HOME_BUDGET)
  expect([HOME_BUDGET, ROUTE_BUDGET]).toEqual([350_000, 500_000])
})

it('prints the table of every route and succeeds within the budget', () => {
  writeApplication()
  const lines = []
  expect(build(() => {}, (line) => lines.push(line))).toBe(0)
  expect(lines).toHaveLength(3)
  expect(lines[0]).toMatch(/^first-page JavaScript per route \(the initial static \d+\.\d\d kB is in every figure\)$/)
  expect(lines[2]).toMatch(/^\/ +\d+\.\d\d kB +350\.00 kB +\d+% +ok$/)
})

it('fails the build, naming the route and the files, when the first page is over the budget, even if the initial scripts are not', () => {
  writeApplication({ primeBytes: HOME_BUDGET })
  const failures = []
  const lines = []
  expect(routeBudgets(dist, ROUTES).initialBytes).toBeLessThan(HOME_BUDGET)
  expect(build((line) => failures.push(line), (line) => lines.push(line))).toBe(1)
  expect(failures.join('')).toContain('/ first-page JavaScript')
  expect(failures.join('')).toContain('over the budget of 350.00 kB')
  expect(failures.join('')).toContain('chunk-PRIME.js')
  expect(lines.at(-1)).toMatch(/OVER$/)
  expect(existsSync(join(dist, '.gitkeep'))).toBe(false)
})

it('passes at exactly the budget', () => {
  writeApplication({ mainBytes: 0 })
  const base = routeBudgets(dist, ROUTES).rows[0].bytes
  writeApplication({ mainBytes: HOME_BUDGET - base })
  expect(routeBudgets(dist, ROUTES).rows[0].bytes).toBe(HOME_BUDGET)
  expect(build()).toBe(0)
})

describe('failing closed', () => {
  it('fails for a script the document names but the build did not emit', () => {
    writeApplication({ extraIndex: '<script src="main-GONE.js" type="module"></script>' })
    const failures = []
    expect(build((line) => failures.push(line))).toBe(1)
    expect(failures.join('')).toContain('main-GONE.js')
  })

  it('fails for a script in a subdirectory or outside dist', () => {
    mkdirSync(join(dist, 'sub'))
    writeFileSync(join(dist, 'sub', 'x.js'), 'x')
    for (const src of ['sub/x.js', '../x.js', '/abs.js']) {
      writeApplication({ extraIndex: `<script src="${src}" type="module"></script>` })
      expect(initialScripts(dist).problems.join(''), src).toContain(src)
    }
  })

  it('fails for a static import of a chunk that is missing', () => {
    writeApplication()
    writeFileSync(join(dist, 'chunk-SIDE.js'), 'import"./chunk-LOST.js"')
    expect(initialScripts(dist).problems.join('')).toContain('chunk-LOST.js')
  })

  it('fails when the document loads no script', () => {
    writeFileSync(join(dist, 'index.html'), `<app-root ngCspNonce="${NONCE_PLACEHOLDER}"></app-root>`)
    const failures = []
    expect(build((line) => failures.push(line))).toBe(1)
    expect(failures.join('')).toContain('loads no script')
  })

  it('fails without the build metafile, and when no output is the first page', () => {
    writeApplication({ stats: false })
    const failures = []
    expect(build((line) => failures.push(line))).toBe(1)
    expect(failures.join('')).toContain('stats.json')
    writeApplication()
    writeFileSync(join(dist, 'stats.json'), JSON.stringify({ outputs: { 'main-ABC.js': {} } }))
    expect(firstPageScripts(dist, ['main-ABC.js'], FIRST_PAGE_ENTRY).problems.join('')).toContain(FIRST_PAGE_ENTRY)
  })
})

describe('the gallery of the preview build', () => {
  it('names what only the gallery contains', () => {
    expect(GALLERY_MARKERS).toEqual(['app-gallery', 'cockpit-gallery-fixture'])
  })

  it('is found in a script directly under dist, by its selector or its fixture marker, and not elsewhere', () => {
    writeApplication()
    expect(galleryLeaks(dist)).toEqual([])
    writeFileSync(join(dist, 'chunk-G1.js'), 'selector:"app-gallery"')
    writeFileSync(join(dist, 'chunk-G2.js'), 'machine:"cockpit-gallery-fixture"')
    writeFileSync(join(dist, 'notes.txt'), 'app-gallery')
    expect(galleryLeaks(dist).sort()).toEqual(['chunk-G1.js', 'chunk-G2.js'])
  })

  it('fails the production build, naming the file, when it is in it', () => {
    writeApplication()
    writeFileSync(join(dist, 'chunk-G1.js'), 'selector:"app-gallery"')
    const messages = []
    expect(build((message) => messages.push(message))).toBe(1)
    expect(messages.join(' ')).toContain('chunk-G1.js')
    expect(messages.join(' ')).toContain('gallery')
  })
})
