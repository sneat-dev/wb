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

  it('loads each page lazily, with no lazy child routes', async () => {
    const lazy = pageRoutes.filter((route): route is Route => route.loadComponent !== undefined)
    expect(pageRoutes.filter((route) => route.loadChildren !== undefined)).toEqual([])
    expect(lazy.map((route) => route.path)).toEqual(['', 'tasks', 'tasks/new', 'tasks/detail', 'repositories', 'repositories/:host/:owner/:name', 'repositories/:id', 'worktrees', 'worktrees/:id', 'agents', 'agents/:id', 'machines', 'machines/:id'])
    for (const route of lazy) {
      const loaded = await (route.loadComponent as () => Promise<unknown>)()
      expect(typeof loaded, route.path).toBe('function')
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
