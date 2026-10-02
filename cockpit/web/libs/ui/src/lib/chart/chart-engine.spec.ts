import { readFileSync } from 'node:fs'
import { join } from 'node:path'

const register = vi.fn()
const instances: { canvas: unknown; config: unknown; data?: unknown; options?: unknown; update: ReturnType<typeof vi.fn>; destroy: ReturnType<typeof vi.fn> }[] = []

vi.mock('chart.js', () => {
  class Chart {
    static register = register
    data: unknown
    options: unknown
    update = vi.fn()
    destroy = vi.fn()
    constructor(canvas: unknown, config: unknown) {
      instances.push(Object.assign(this, { canvas, config }))
    }
  }
  const part = (name: string) => ({ id: name })
  return {
    Chart,
    BarController: part('BarController'),
    BarElement: part('BarElement'),
    LineController: part('LineController'),
    LineElement: part('LineElement'),
    PointElement: part('PointElement'),
    CategoryScale: part('CategoryScale'),
    LinearScale: part('LinearScale'),
    Filler: part('Filler'),
    Tooltip: part('Tooltip'),
  }
})

describe('the Chart.js engine', () => {
  // cockpit-views#ac:chart-library-is-pinned-and-tree-shaken
  it('registers only the controllers, elements, scales, filler and tooltip the three presets use', async () => {
    await import('./chart-engine')
    expect(register).toHaveBeenCalledTimes(1)
    expect(register.mock.calls[0].map((part: { id: string }) => part.id).sort()).toEqual(
      ['BarController', 'BarElement', 'CategoryScale', 'Filler', 'LineController', 'LineElement', 'LinearScale', 'PointElement', 'Tooltip'].sort(),
    )
  })

  it('imports Chart.js by name, and nowhere else, so the bundler keeps nothing it does not use', () => {
    const sources = ['chart-engine.ts', 'chart-config.ts', 'chart-view.ts', 'chart-spec.ts', 'chart-theme.ts'].map((file) => [file, readFileSync(join(__dirname, file), 'utf8')])
    for (const [file, source] of sources) {
      for (const match of source.matchAll(/(?:import|from)\s+(type\s+)?[^;\n]*?['"]chart\.js(\/[^'"]*)?['"]/g)) {
        expect(match[2], `${file} must not import a Chart.js subpath such as chart.js/auto`).toBeUndefined()
        if (file !== 'chart-engine.ts') expect(match[1], `${file} may import Chart.js types only`).toBeTruthy()
      }
    }
    expect(sources.find(([file]) => file === 'chart-view.ts')?.[1]).toContain("import('./chart-engine')")
  })

  it('draws a chart on the canvas it is given, redraws without animation and destroys it', async () => {
    const { create } = await import('./chart-engine')
    const canvas = document.createElement('canvas')
    const config = { type: 'bar', data: { datasets: [] }, options: { responsive: true } } as never
    const chart = create(canvas, config)
    expect(instances[0].canvas).toBe(canvas)
    expect(instances[0].config).toBe(config)
    chart.update({ type: 'bar', data: { datasets: [{ data: [1] }] }, options: { responsive: false } } as never)
    expect(instances[0].data).toEqual({ datasets: [{ data: [1] }] })
    expect(instances[0].options).toEqual({ responsive: false })
    expect(instances[0].update).toHaveBeenCalledWith('none')
    chart.update({ type: 'bar', data: { datasets: [] } } as never)
    expect(instances[0].options).toEqual({})
    chart.destroy()
    expect(instances[0].destroy).toHaveBeenCalled()
  })
})
