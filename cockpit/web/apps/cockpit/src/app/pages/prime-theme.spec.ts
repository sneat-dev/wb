import { EnvironmentInjector, createEnvironmentInjector } from '@angular/core'
import { TestBed } from '@angular/core/testing'
import { PrimeNG } from 'primeng/config'
import { pageRoutes } from '../app.routes'
import { CockpitPreset, primePage, runInitializers } from './prime-theme'

describe('primePage', () => {
  it('routes to the page, and configures PrimeNG with the theme and the style nonce when the route is created, not at bootstrap', async () => {
    document.body.innerHTML = '<app-root ngCspNonce="nonce-1"></app-root>'
    class Page {}
    const [route] = primePage(async () => Page)
    expect(route.path).toBe('')
    expect(await (route.loadComponent as () => Promise<unknown>)()).toBe(Page)
    const injector = createEnvironmentInjector(route.providers as never[], TestBed.inject(EnvironmentInjector))
    const config = injector.get(PrimeNG)
    expect(config.csp().nonce).toBe('nonce-1')
    expect(config.theme()).toMatchObject({ options: { darkModeSelector: 'system' } })
    injector.destroy()
    document.body.innerHTML = ''
  })
})

describe('the PrimeUI licence check', () => {
  it('runs for a route that loads PrimeNG, which shows its own notice when the licence is not valid', async () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => undefined)
    const [route] = primePage(async () => class {})
    const injector = createEnvironmentInjector(route.providers as never[], TestBed.inject(EnvironmentInjector))
    await vi.waitFor(() => expect(warn.mock.calls.some((call) => String(call[0]).includes('[PrimeUI]'))).toBe(true))
    injector.destroy()
    warn.mockRestore()
  })

  it('is not run, and PrimeNG is not loaded, for Home or any page that uses none', () => {
    for (const path of ['', 'tasks', 'tasks/new', 'tasks/detail', 'worktrees', 'worktrees/:id', 'agents/:id', 'machines/:id', 'repositories/:id']) {
      const route = pageRoutes.find((candidate) => candidate.path === path)
      expect(route?.loadChildren, path).toBeUndefined()
      expect(route?.providers, path).toBeUndefined()
    }
    const primeRoutes = pageRoutes.filter((route) => route.loadChildren !== undefined).map((route) => route.path)
    expect(primeRoutes).toEqual(['repositories', 'agents', 'machines'])
  })

  it('runs no initializer in an injector that has none', () => {
    expect(() => TestBed.runInInjectionContext(() => runInitializers())).not.toThrow()
  })
})

describe('CockpitPreset', () => {
  it('points PrimeNG at the design tokens', () => {
    const semantic = CockpitPreset.semantic as Record<string, Record<string, unknown>>
    expect(semantic['primary']['color']).toBe('var(--accent)')
    expect(semantic['text']['color']).toBe('var(--text)')
    expect(semantic['content']['background']).toBe('var(--surface)')
    expect((CockpitPreset.components as Record<string, unknown>)['datatable']).toBeDefined()
  })
})
