import { Route } from '@angular/router'
import { appRoutes } from './app.routes'

describe('appRoutes', () => {
  it('redirects the base and unknown paths to the dashboard', () => {
    expect(appRoutes.find((route) => route.path === '')?.redirectTo).toBe('dashboard')
    expect(appRoutes.find((route) => route.path === '**')?.redirectTo).toBe('dashboard')
  })

  it('loads each page lazily, a detail page under its list with the path parameter named id', async () => {
    const pages = appRoutes.filter((route): route is Route => route.loadComponent !== undefined)
    expect(pages.map((route) => route.path)).toEqual(['dashboard', 'repositories', 'repositories/:id', 'worktrees', 'worktrees/:id', 'agents', 'machines'])
    for (const route of pages) {
      const loaded = await (route.loadComponent as () => Promise<unknown>)()
      expect(typeof loaded).toBe('function')
    }
  })
})
