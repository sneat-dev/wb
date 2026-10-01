import { Route } from '@angular/router'
import { appRoutes, pageRoutes } from './app.routes'
import { PAGE_LINKS } from './nav'

describe('pageRoutes', () => {
  it('registers every route of the application, so a page task never edits the table', () => {
    expect(pageRoutes.map((route) => route.path)).toEqual([
      '',
      'dashboard',
      'tasks',
      'tasks/new',
      'tasks/detail',
      'repositories',
      'repositories/:host/:owner/:name',
      'repositories/:id',
      'worktrees',
      'worktrees/:id',
      'agents',
      'agents/:id',
      'machines',
      'machines/:id',
      '**',
    ])
  })

  it('shows Home at the root, keeps /dashboard as an alias for it and sends unknown paths there', () => {
    const home = pageRoutes.find((route) => route.path === '') as Route
    expect(home.title).toBe('Home')
    expect(home.pathMatch).toBe('full')
    expect(pageRoutes.find((route) => route.path === 'dashboard')?.redirectTo).toBe('')
    expect(pageRoutes.find((route) => route.path === '**')?.redirectTo).toBe('')
    expect(pageRoutes.some((route) => route.title === 'Dashboard')).toBe(false)
  })

  it('has a route for every tab, titled with the tab', () => {
    for (const link of PAGE_LINKS) {
      const route = pageRoutes.find((candidate) => `/${candidate.path}` === link.path || (link.path === '/' && candidate.path === ''))
      expect(route?.title, link.path).toBe(link.label)
    }
  })

  it('loads each page lazily, those that still use PrimeNG through the route that provides it', async () => {
    const plain = pageRoutes.filter((route): route is Route => route.loadComponent !== undefined)
    const prime = pageRoutes.filter((route): route is Route => route.loadChildren !== undefined)
    expect(plain.map((route) => route.path)).toEqual(['', 'tasks', 'tasks/new', 'tasks/detail', 'repositories', 'repositories/:host/:owner/:name', 'repositories/:id', 'worktrees', 'worktrees/:id', 'agents/:id', 'machines/:id'])
    expect(prime.map((route) => route.path)).toEqual(['agents', 'machines'])
    for (const route of plain) {
      const loaded = await (route.loadComponent as () => Promise<unknown>)()
      expect(typeof loaded, route.path).toBe('function')
    }
    for (const route of prime) {
      const [child] = await (route.loadChildren as () => Promise<Route[]>)()
      expect(typeof (await (child.loadComponent as () => Promise<unknown>)()), route.path).toBe('function')
      expect(child.providers, route.path).toBeDefined()
    }
  })

  it('names the path parameter of a detail route id where it has one', () => {
    for (const route of pageRoutes) {
      const parameters = (route.path ?? '').split('/').filter((segment) => segment.startsWith(':'))
      if (route.path === 'repositories/:host/:owner/:name') expect(parameters).toEqual([':host', ':owner', ':name'])
      else expect(parameters.every((parameter) => parameter === ':id')).toBe(true)
    }
  })
})

describe('appRoutes', () => {
  it('is the page table', () => {
    expect(appRoutes).toBe(pageRoutes)
  })
})
