import { EnvironmentInjector, createEnvironmentInjector } from '@angular/core'
import { TestBed } from '@angular/core/testing'
import { PrimeNG } from 'primeng/config'
import { CockpitPreset, runInitializers, withPrimeNg } from './prime-theme'

describe('withPrimeNg', () => {
  it('wraps the routes in one group that provides PrimeNG with the Cockpit theme', () => {
    const routes = [{ path: 'x' }]
    const [group] = withPrimeNg(routes)
    expect(group.path).toBe('')
    expect(group.providers).toHaveLength(2)
    expect(group.children).toBe(routes)
  })

  it('configures PrimeNG with the theme and the style nonce when the group is created, not at bootstrap', () => {
    document.body.innerHTML = '<app-root ngCspNonce="nonce-1"></app-root>'
    const [group] = withPrimeNg([])
    const injector = createEnvironmentInjector(group.providers as never[], TestBed.inject(EnvironmentInjector))
    const config = injector.get(PrimeNG)
    expect(config.csp().nonce).toBe('nonce-1')
    expect(config.theme()).toMatchObject({ options: { darkModeSelector: 'system' } })
    injector.destroy()
    document.body.innerHTML = ''
  })
})

describe('runInitializers', () => {
  it('runs none in an injector that has none', () => {
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
