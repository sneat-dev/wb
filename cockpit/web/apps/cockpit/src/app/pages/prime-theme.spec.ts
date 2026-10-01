import { CockpitPreset, withPrimeNg } from './prime-theme'

describe('withPrimeNg', () => {
  it('wraps the routes in one group that provides PrimeNG with the Cockpit theme', () => {
    const routes = [{ path: 'x' }]
    const [group] = withPrimeNg(routes)
    expect(group.path).toBe('')
    expect(group.providers).toHaveLength(1)
    expect(group.children).toBe(routes)
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
