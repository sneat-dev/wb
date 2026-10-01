// What `pnpm shots` photographs: every route of the application, in light and
// dark, at a desktop and a phone size, and the overlays and states that are not
// a route. The plan is pure data; tools/shots.mjs drives the browser.

export const VIEWPORTS = [
  { name: '1440', width: 1440, height: 900 },
  { name: '390', width: 390, height: 844 },
  // The narrowest width the application works at (REQ:responsive-to-360).
  { name: '360', width: 360, height: 800 },
]

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
