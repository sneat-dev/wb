import { Route } from '@angular/router'
import { appRoutes } from './app.routes'

describe('appRoutes', () => {
  it('redirects the base and unknown paths to the dashboard', () => {
    expect(appRoutes.find((route) => route.path === '')?.redirectTo).toBe('dashboard')
    expect(appRoutes.find((route) => route.path === '**')?.redirectTo).toBe('dashboard')
  })

  it('loads each of the five pages lazily', async () => {
    const pages = appRoutes.filter((route): route is Route => route.loadComponent !== undefined)
    expect(pages.map((route) => route.path)).toEqual(['dashboard', 'repositories', 'worktrees', 'agents', 'machines'])
    for (const route of pages) {
      const loaded = await (route.loadComponent as () => Promise<unknown>)()
      expect(typeof loaded).toBe('function')
    }
  })
})
