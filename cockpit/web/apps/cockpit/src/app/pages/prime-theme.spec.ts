import { EnvironmentInjector, createEnvironmentInjector } from '@angular/core'
import { TestBed } from '@angular/core/testing'
import { PrimeNG } from 'primeng/config'
import { CockpitPreset, primePage } from './prime-theme'

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

describe('CockpitPreset', () => {
  it('points PrimeNG at the design tokens', () => {
    const semantic = CockpitPreset.semantic as Record<string, Record<string, unknown>>
    expect(semantic['primary']['color']).toBe('var(--accent)')
    expect(semantic['text']['color']).toBe('var(--text)')
    expect(semantic['content']['background']).toBe('var(--surface)')
    expect((CockpitPreset.components as Record<string, unknown>)['datatable']).toBeDefined()
  })
})
