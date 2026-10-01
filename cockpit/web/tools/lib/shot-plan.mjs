// What `pnpm shots` photographs: every route of the application, in light and
// dark, at a desktop and a phone size, and the overlays and states that are not
// a route. The plan is pure data; tools/shots.mjs drives the browser.

export const VIEWPORTS = [
  { name: '1440', width: 1440, height: 900 },
  { name: '390', width: 390, height: 844 },
  // The narrowest width the application works at (REQ:responsive-to-360).
  { name: '360', width: 360, height: 800 },
]

// The lists are also photographed at a tablet width, where a panel beside them leaves the columns little room.
export const LIST_VIEWPORTS = [VIEWPORTS[0], { name: '1024', width: 1024, height: 768 }, ...VIEWPORTS.slice(1)]

export const SCHEMES = ['light', 'dark']

// The routes of the application, with the ids of the first entries of `document`
// filling the detail routes. `name` is the file stem.
export function routePlan(document) {
  const repository = document.repositories.find((candidate) => candidate.host) ?? document.repositories[0]
  const worktree = document.worktrees[0]
  const agent = document.agents[0]
  const machine = document.machines[0]
  const detailOfRepository = `/repositories/${[repository.host ?? '-', ...repository.name.split('/')].map(encodeURIComponent).join('/')}`
  return [
    { name: 'home', url: '/' },
    { name: 'tasks', url: '/tasks' },
    { name: 'tasks-new', url: '/tasks/new' },
    { name: 'task-detail', url: `/tasks/detail?task=${encodeURIComponent(worktree.task)}` },
    { name: 'repositories', url: '/repositories' },
    { name: 'repository-by-name', url: detailOfRepository },
    { name: 'repository-by-id', url: `/repositories/${encodeURIComponent(repository.id)}` },
    { name: 'worktrees', url: '/worktrees' },
    { name: 'worktree-detail', url: `/worktrees/${encodeURIComponent(worktree.id)}` },
    { name: 'agents', url: '/agents' },
    { name: 'agent-detail', url: `/agents/${encodeURIComponent(agent.id)}` },
    { name: 'machines', url: '/machines' },
    { name: 'machine-detail', url: `/machines/${encodeURIComponent(machine.id)}` },
  ]
}

// The shared list and its side panel, on the Worktrees page: what the operator does
// (`steps`, run by tools/shots.mjs) before the picture is taken. `filter:<text>` types
// in the filter, `chip:<label>` toggles a quick filter, `row:<n>` selects the nth row
// and `raw` opens the "Raw data" block; `select:<n>` selects the nth row with the keyboard (for a list whose
// last cell holds controls) and `radio:<label>` chooses a radio button. A shot's `url` is its page; Worktrees by default.
export const LIST_SHOTS = [
  { name: 'worktrees-list', steps: [] },
  { name: 'worktrees-filter', steps: ['filter:fix'] },
  { name: 'worktrees-chip', steps: ['chip:Active'] },
  { name: 'worktrees-no-match', steps: ['filter:zzzzqq'] },
  { name: 'worktrees-panel', steps: ['row:2'] },
  { name: 'worktrees-panel-raw', steps: ['row:2', 'raw'] },
  { name: 'repositories-list', url: '/repositories', steps: [] },
  { name: 'repositories-sort', url: '/repositories', steps: ['radio:Most worktrees'] },
  { name: 'repositories-chip', url: '/repositories', steps: ['chip:Index needs a look'] },
  { name: 'repositories-no-match', url: '/repositories', steps: ['filter:zzzzqq'] },
  { name: 'repositories-panel', url: '/repositories', steps: ['select:1'] },
  { name: 'repositories-panel-raw', url: '/repositories', steps: ['select:1', 'raw'] },
  { name: 'tasks-list', url: '/tasks', steps: [] },
  { name: 'tasks-chip', url: '/tasks', steps: ['chip:Needs you'] },
  { name: 'tasks-no-match', url: '/tasks', steps: ['filter:zzzzqq'] },
  { name: 'tasks-panel', url: '/tasks', steps: ['row:1'] },
  { name: 'tasks-panel-raw', url: '/tasks', steps: ['row:1', 'raw'] },
  { name: 'agents-list', url: '/agents', steps: [] },
  { name: 'agents-chip', url: '/agents', steps: ['chip:Blocked'] },
  { name: 'agents-no-match', url: '/agents', steps: ['filter:zzzzqq'] },
  { name: 'agents-panel', url: '/agents', steps: ['row:3'] },
  { name: 'agents-panel-raw', url: '/agents', steps: ['row:3', 'raw'] },
  { name: 'machines-list', url: '/machines', steps: [] },
  { name: 'machines-chip', url: '/machines', steps: ['chip:Stale'] },
  { name: 'machines-no-match', url: '/machines', steps: ['filter:zzzzqq'] },
  { name: 'machines-panel', url: '/machines', steps: ['row:0'] },
  { name: 'machines-panel-raw', url: '/machines', steps: ['row:0', 'raw'] },
]

// Every shot: the route plan in each scheme and size, then the overlays on Home,
// then the states of the daemon. A `state` shot needs a server in that state.
export function shotPlan(document) {
  const shots = []
  for (const route of routePlan(document)) {
    for (const scheme of SCHEMES) {
      for (const viewport of VIEWPORTS) shots.push({ ...route, state: 'ok', scheme, viewport, file: `${route.name}-${scheme}-${viewport.name}.png` })
    }
  }
  for (const scheme of SCHEMES) {
    for (const viewport of VIEWPORTS) {
      for (const [name, keys] of [['palette', ['Control+k']], ['palette-results', ['Control+k', 'type:go']], ['shortcuts', ['?']]]) {
        shots.push({ name, url: '/', state: 'ok', keys, scheme, viewport, file: `${name}-${scheme}-${viewport.name}.png` })
      }
      for (const state of ['warming', 'daemon-older']) shots.push({ name: `state-${state}`, url: '/', state, scheme, viewport, file: `state-${state}-${scheme}-${viewport.name}.png` })
    }
  }
  // Each machine's page whole, once its charts have drawn (the local machine has a history, the live one too, the old one a cached sample).
  for (const machine of document.machines.slice(0, 3)) {
    for (const scheme of SCHEMES) {
      for (const viewport of LIST_VIEWPORTS) {
        const name = `machine-full-${machine.machine.replace(/[^A-Za-z0-9-]/g, '-')}`
        shots.push({ name, url: `/machines/${encodeURIComponent(machine.id)}`, state: 'ok', scheme, viewport, file: `${name}-${scheme}-${viewport.name}.png`, fullPage: true, scrollEnd: true })
      }
    }
  }
  for (const list of LIST_SHOTS) {
    for (const scheme of SCHEMES) {
      for (const viewport of LIST_VIEWPORTS) shots.push({ ...list, url: list.url ?? '/worktrees', state: 'ok', scheme, viewport, file: `${list.name}-${scheme}-${viewport.name}.png` })
    }
  }
  return shots
}

// Home is photographed against hand-made fleets (apps/cockpit/src/app/pages/home/home-fixtures.ts): each case is a fleet and what the
// preview serves around it (the session and the action registry), at the sizes Home is designed for.
export const HOME_VIEWPORTS = [
  { name: '1440', width: 1440, height: 900 },
  { name: '1024', width: 1024, height: 768 },
  { name: '390', width: 390, height: 844 },
  { name: '360', width: 360, height: 800 },
]

export const HOME_CASES = [
  // The fleet, then who is looking: an anonymous reader with no registry (copy commands) and an owner with one (action slots).
  { name: 'busy', home: 'busy' },
  { name: 'busy-owner', home: 'busy', session: 'owner', registry: true },
  { name: 'healthy', home: 'healthy' },
  { name: 'warming', home: 'warming', state: 'warming' },
  { name: 'throttled', home: 'throttled' },
  { name: 'remote-error', home: 'remote-error' },
  { name: 'no-throughput', home: 'no-throughput' },
  { name: 'only-dropped', home: 'only-dropped' },
]

// Every case in light and dark at every Home size, whole page and scrolled to the end so the charts have drawn; the
// phone also gets the "More" disclosure opened.
export function homeShotPlan() {
  const shots = []
  for (const home of HOME_CASES) {
    for (const scheme of SCHEMES) {
      for (const viewport of HOME_VIEWPORTS) {
        const base = { url: '/', state: home.state ?? 'ok', session: home.session, registry: home.registry, home: home.home, scheme, viewport, fullPage: true, ready: 'app-home-rest', scrollEnd: true }
        shots.push({ ...base, name: `home-${home.name}`, file: `home-${home.name}-${scheme}-${viewport.name}.png` })
        if (viewport.width <= 480 && home.home === 'busy' && home.name === 'busy') {
          shots.push({ ...base, name: `home-${home.name}-more`, file: `home-${home.name}-more-${scheme}-${viewport.name}.png`, click: 'button.home-more-toggle' })
        }
      }
    }
  }
  return shots
}

// The gallery of the control surface: a route of the preview build only
// (`pnpm build:preview`, served from dist-preview), photographed whole, once the
// charts have drawn, in each scheme and size.
export const GALLERY_DIST = 'dist-preview'

export function galleryPlan() {
  const shots = []
  for (const scheme of SCHEMES) {
    for (const viewport of VIEWPORTS) {
      shots.push({ name: 'gallery', url: '/gallery', state: 'ok', scheme, viewport, file: `gallery-${scheme}-${viewport.name}.png`, dist: GALLERY_DIST, fullPage: true, ready: 'canvas' })
    }
  }
  return shots
}
