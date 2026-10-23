// The first-page JavaScript budget of every route (cockpit-views#req:initial-script-size), measured one way: the scripts the
// entry document loads at once, with what they import statically, plus the chunks of the route's own page (its entry point
// in the build metafile and what that imports statically, with the lazy chunks that load it), counted raw over JavaScript
// files only. The routes are read from app.routes.ts, so a route added there is measured without editing a list here.
import { firstPageScripts, initialScripts, kilobytes, sizeOf, total } from './script-graph.mjs'

/** Home, the first thing the operator sees, in the kilobytes the Angular build report uses (1,000 bytes). */
export const HOME_BUDGET = 350_000
/** Every other route. */
export const ROUTE_BUDGET = 500_000

/** The page of the default route: its source file under apps/cockpit/src/app. */
export const FIRST_PAGE_ENTRY = 'pages/home/home-page.ts'

// `page('tasks/new', 'New task', () => import('./pages/new-task/new-task-page').then(...))`, also behind a spread.
const PAGE_ROUTE = /\bpage\(\s*'([^']*)'\s*,\s*'[^']*'\s*,\s*\(\)\s*=>\s*import\('\.\/(pages\/[^']+)'\)/g

/** Every lazy page route of the routes source as the address it serves and the source file of its component. */
export function routeEntries(routesSource) {
  return [...routesSource.matchAll(PAGE_ROUTE)].map((match) => ({ route: `/${match[1]}`, entry: `${match[2]}.ts` }))
}

/** The budget of a route: Home's, or the one every other route shares. */
export const budgetOf = (route) => (route === '/' ? HOME_BUDGET : ROUTE_BUDGET)

/**
 * The first-page JavaScript of every route against its budget. It fails closed: a build without a metafile, a route whose
 * page is not an output of it, and a routes source with no route (or none for Home) are problems, so a build that lost
 * the means to measure cannot pass.
 */
export function routeBudgets(dist, routesSource) {
  const entries = routeEntries(routesSource)
  const problems = []
  if (!entries.some((entry) => entry.route === '/')) problems.push('app.routes.ts has no page route for Home')
  const initial = initialScripts(dist)
  problems.push(...initial.problems)
  const initialFiles = sizeOf(dist, initial.files)
  const rows = []
  if (problems.length === 0) {
    for (const { route, entry } of entries) {
      const first = firstPageScripts(dist, initial.files, entry)
      if (first.problems.length > 0) {
        problems.push(`${route}: ${first.problems.join('; ')}`)
        continue
      }
      const files = sizeOf(dist, first.files)
      const bytes = total(files)
      rows.push({ route, entry, files, bytes, budget: budgetOf(route), over: bytes > budgetOf(route) })
    }
  }
  return { problems, initialBytes: total(initialFiles), rows }
}

/** The table printed at the end of the build: one line per route, with how much of its budget it uses and the verdict. */
export function budgetTable(result) {
  const width = Math.max(5, ...result.rows.map((row) => row.route.length))
  const line = (route, first, budget, used, verdict) => `${route.padEnd(width)}  ${first.padStart(10)}  ${budget.padStart(10)}  ${used.padStart(5)}  ${verdict}`
  return [
    `first-page JavaScript per route (the initial static ${kilobytes(result.initialBytes)} is in every figure)`,
    line('route', 'first page', 'budget', 'used', ''),
    ...result.rows.map((row) => line(row.route, kilobytes(row.bytes), kilobytes(row.budget), `${Math.round((row.bytes / row.budget) * 100)}%`, row.over ? 'OVER' : 'ok')),
  ]
}

/** What fails the build: one line per route over its budget, with the files that make it up. */
export function overBudget(result) {
  return result.rows
    .filter((row) => row.over)
    .map((row) => `${row.route} first-page JavaScript is ${kilobytes(row.bytes)}, over the budget of ${kilobytes(row.budget)} (${row.files.map((file) => `${file.name} ${kilobytes(file.bytes)}`).join(', ')}); load page code lazily`)
}
