import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { Chart } from 'chart.js'
import '../chart/chart-engine'

// Against the real chart.js module, with no mock.
describe('Chart.js as the application uses it', () => {
  // cockpit-views#ac:chart-library-is-pinned-and-tree-shaken
  it('is listed in package.json at an exact version, the one installed, with no range', () => {
    const manifest = JSON.parse(readFileSync(join(__dirname, '../../../../../package.json'), 'utf8')) as { dependencies: Record<string, string> }
    const pinned = manifest.dependencies['chart.js']
    expect(pinned).toMatch(/^\d+\.\d+\.\d+$/)
    const installed = JSON.parse(readFileSync(join(__dirname, '../../../../../node_modules/chart.js/package.json'), 'utf8')) as { version: string }
    expect(installed.version).toBe(pinned)
  })

  it('has registered exactly the controllers, elements, scales and plugins the three presets use, and nothing else', () => {
    const registry = Chart.registry
    for (const id of ['bar', 'line']) expect(registry.getController(id).id).toBe(id)
    for (const id of ['bar', 'line', 'point']) expect(registry.getElement(id)).toBeDefined()
    for (const id of ['category', 'linear']) expect(registry.getScale(id).id).toBe(id)
    for (const id of ['filler', 'tooltip']) expect(registry.getPlugin(id)?.id).toBe(id)
    for (const id of ['pie', 'doughnut', 'radar', 'polarArea', 'bubble', 'scatter']) expect(() => registry.getController(id), id).toThrow()
    for (const id of ['time', 'timeseries', 'logarithmic', 'radialLinear']) expect(() => registry.getScale(id), id).toThrow()
    for (const id of ['legend', 'title', 'subtitle', 'decimation']) expect(() => registry.getPlugin(id), id).toThrow()
    for (const id of ['arc']) expect(() => registry.getElement(id), id).toThrow()
  })
})
