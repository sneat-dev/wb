import { TestBed } from '@angular/core/testing'
import type { AppLink } from '@cockpit/fleet-data'
import type { ChartConfiguration } from 'chart.js'
import type { ChartEngine } from './chart-engine'
import { CHART_ENGINE, ChartView } from './chart-view'
import { ChartSpec } from './chart-spec'

const link: AppLink = { path: '/worktrees', query: { q: 'age:<1d' } }
const buckets: ChartSpec = { kind: 'horizontal-bars', title: 'Worktree age', valueLabel: 'Worktrees', bars: [{ label: '< 1 d', value: 4, link }, { label: '1-7 d', value: 0 }] }
const days: ChartSpec = { kind: 'bars', title: 'Landed per day', valueLabel: 'Landed', bars: [{ label: 'Mon', value: 2 }] }
const series: ChartSpec = { kind: 'time-series', title: 'CPU', valueLabel: 'CPU %', unit: '%', from: 0, to: 1, points: [{ at: 0, value: 5 }, { at: 1, value: null }] }

interface Media {
  matches: boolean
  listeners: Set<() => void>
}

function stubMedia(reduced: boolean): Record<string, Media> {
  const queries: Record<string, Media> = {}
  vi.stubGlobal('matchMedia', (query: string) => {
    const media = (queries[query] ??= { matches: query.includes('reduced-motion') ? reduced : false, listeners: new Set() })
    return {
      get matches() {
        return media.matches
      },
      addEventListener: (_: string, listener: () => void) => media.listeners.add(listener),
      removeEventListener: (_: string, listener: () => void) => media.listeners.delete(listener),
    }
  })
  return queries
}

function setup() {
  const chart = { update: vi.fn(), destroy: vi.fn() }
  const create = vi.fn<(canvas: HTMLCanvasElement, config: ChartConfiguration) => typeof chart>(() => chart)
  let release: (engine: ChartEngine) => void = () => undefined
  const loading = new Promise<ChartEngine>((resolve) => (release = resolve))
  TestBed.resetTestingModule()
  TestBed.configureTestingModule({ providers: [{ provide: CHART_ENGINE, useValue: () => loading }] })
  return { chart, create, load: () => release({ create }) }
}

async function render(spec: ChartSpec, height?: number) {
  const parts = setup()
  const fixture = TestBed.createComponent(ChartView)
  fixture.componentRef.setInput('spec', spec)
  if (height !== undefined) fixture.componentRef.setInput('height', height)
  const selected: AppLink[] = []
  fixture.componentInstance.bucketSelected.subscribe((value) => selected.push(value))
  await fixture.whenStable()
  const root: HTMLElement = fixture.nativeElement
  const loaded = async () => {
    parts.load()
    await vi.waitFor(() => expect(parts.create).toHaveBeenCalled())
  }
  return { ...parts, fixture, root, selected, loaded }
}

describe('ChartView', () => {
  afterEach(() => vi.unstubAllGlobals())

  // cockpit-views#ac:csp-and-canvas-only
  it('draws on a canvas only and has no script or inline style attribute of its own', async () => {
    stubMedia(false)
    const { root, loaded } = await render(buckets)
    await loaded()
    expect(root.querySelectorAll('canvas')).toHaveLength(1)
    expect(root.querySelector('script, style, svg, img')).toBeNull()
    expect(root.querySelector('canvas')?.getAttribute('style')).toBeNull()
  })

  it('always carries a text alternative: an accessible name for the canvas and the same numbers as a table', async () => {
    stubMedia(false)
    const { root } = await render(buckets)
    expect(root.querySelector('canvas')?.getAttribute('role')).toBe('img')
    expect(root.querySelector('canvas')?.getAttribute('aria-label')).toBe('Worktree age: 2 buckets, 4 in all')
    expect(root.querySelector('figcaption')?.textContent).toBe('Worktree age')
    expect(root.querySelector('caption')?.textContent?.trim()).toBe('Worktree age')
    expect([...root.querySelectorAll('thead th')].map((cell) => cell.textContent)).toEqual(['Bucket', 'Worktrees'])
    expect([...root.querySelectorAll('tbody tr')].map((row) => [...row.children].map((cell) => cell.textContent?.trim()))).toEqual([['< 1 d', '4'], ['1-7 d', '0']])
    expect(root.querySelector('.data')).not.toBeNull()
  })

  it('says "No data" in the table for an empty chart', async () => {
    stubMedia(false)
    const { root } = await render({ ...days, kind: 'bars', bars: [] })
    expect(root.querySelector('tbody')?.textContent?.trim()).toBe('No data')
  })

  it('keeps one fixed plot height, so the chart arriving moves nothing', async () => {
    stubMedia(false)
    expect((await render(days)).root.querySelector<HTMLElement>('.plot')?.style.height).toBe('9rem')
    expect((await render(days, 12)).root.querySelector<HTMLElement>('.plot')?.style.height).toBe('12rem')
  })

  it('loads the engine after the first render and draws the spec on the canvas, themed from the tokens', async () => {
    stubMedia(false)
    const { create, root, loaded } = await render(series)
    expect(create).not.toHaveBeenCalled()
    await loaded()
    expect(create).toHaveBeenCalledTimes(1)
    expect(create.mock.calls[0][0]).toBe(root.querySelector('canvas'))
    expect(create.mock.calls[0][1].type).toBe('line')
    expect(create.mock.calls[0][1].options?.animation).toEqual({ duration: 240 })
  })

  it('does not animate for a viewer who asked for reduced motion', async () => {
    stubMedia(true)
    const { create, loaded } = await render(days)
    await loaded()
    expect(create.mock.calls[0][1].options?.animation).toBe(false)
  })

  it('draws with no animation either where matchMedia does not exist', async () => {
    vi.stubGlobal('matchMedia', undefined)
    const { create, loaded } = await render(days)
    await loaded()
    expect(create.mock.calls[0][1].options?.animation).toEqual({ duration: 240 })
  })

  it('redraws the same chart when its data changes, and when the colour scheme or the motion preference changes', async () => {
    const queries = stubMedia(false)
    const { fixture, create, chart, loaded } = await render(days)
    await loaded()
    fixture.componentRef.setInput('spec', { ...days, bars: [{ label: 'Tue', value: 9 }] })
    await fixture.whenStable()
    expect(chart.update).toHaveBeenCalledTimes(1)
    expect((chart.update.mock.calls[0][0] as ChartConfiguration).type).toBe('bar')
    for (const query of ['(prefers-color-scheme: dark)', '(prefers-reduced-motion: reduce)']) {
      const before = chart.update.mock.calls.length
      queries[query].matches = query.includes('reduced')
      queries[query].listeners.forEach((listener) => listener())
      await fixture.whenStable()
      expect(chart.update.mock.calls.length, query).toBe(before + 1)
    }
    expect((chart.update.mock.calls.at(-1)?.[0] as ChartConfiguration).options?.animation).toBe(false)
    expect(create).toHaveBeenCalledTimes(1)
    expect(chart.destroy).not.toHaveBeenCalled()
  })

  it('destroys the chart and makes a new one when the kind of chart changes, because a chart cannot change its type in place', async () => {
    stubMedia(false)
    const { fixture, create, chart, loaded } = await render(days)
    await loaded()
    fixture.componentRef.setInput('spec', series)
    await fixture.whenStable()
    expect(chart.destroy).toHaveBeenCalledTimes(1)
    expect(create).toHaveBeenCalledTimes(2)
    expect(create.mock.calls[1][1].type).toBe('line')
    expect(chart.update).not.toHaveBeenCalled()
    // The same kind again updates the new chart.
    fixture.componentRef.setInput('spec', { ...series, points: [{ at: 0, value: 7 }] })
    await fixture.whenStable()
    expect(create).toHaveBeenCalledTimes(2)
    expect(chart.update).toHaveBeenCalledTimes(1)
  })

  it('says "Chart unavailable" when the Chart.js chunk cannot be fetched, and the data table stays', async () => {
    stubMedia(false)
    TestBed.resetTestingModule()
    TestBed.configureTestingModule({ providers: [{ provide: CHART_ENGINE, useValue: () => Promise.reject(new Error('offline')) }] })
    const fixture = TestBed.createComponent(ChartView)
    fixture.componentRef.setInput('spec', days)
    await fixture.whenStable()
    await vi.waitFor(() => {
      fixture.detectChanges()
      expect(fixture.nativeElement.querySelector('.overlay')?.textContent).toContain('Chart unavailable')
    })
    expect(fixture.nativeElement.querySelector('.overlay')?.getAttribute('role')).toBe('status')
    expect(fixture.nativeElement.querySelectorAll('tbody tr')).toHaveLength(1)
  })

  it('stays quiet about a failed load once destroyed', async () => {
    stubMedia(false)
    let fail: (error: Error) => void = () => undefined
    TestBed.resetTestingModule()
    TestBed.configureTestingModule({ providers: [{ provide: CHART_ENGINE, useValue: () => new Promise((_, reject) => (fail = reject)) }] })
    const fixture = TestBed.createComponent(ChartView)
    fixture.componentRef.setInput('spec', days)
    await fixture.whenStable()
    fixture.destroy()
    fail(new Error('offline'))
    await new Promise((resolve) => setTimeout(resolve))
    expect(fixture.componentInstance['unavailable']()).toBe(false)
  })

  it('says "No data" over an empty chart, drops values that are not finite numbers, and keeps the plot', async () => {
    stubMedia(false)
    const empty = await render({ ...days, bars: [{ label: 'a', value: Number.NaN }, { label: 'b', value: Infinity }] })
    expect(empty.root.querySelector('.overlay')?.textContent).toBe('No data')
    expect(empty.root.querySelector('.plot')).not.toBeNull()
    await empty.loaded()
    expect((empty.create.mock.calls[0][1].data.labels as string[])).toEqual([])
    const gaps = await render({ ...series, points: [{ at: 0, value: Number.NaN }, { at: 1, value: 4 }, { at: Number.NaN, value: 3 }] })
    await gaps.loaded()
    expect(gaps.create.mock.calls[0][1].data.datasets[0].data).toEqual([{ x: 0, y: null }, { x: 1, y: 4 }])
    expect(gaps.root.querySelector('.overlay')).toBeNull()
  })

  it('names a clickable bucket button with its label and its value', async () => {
    stubMedia(false)
    const { root } = await render(buckets)
    expect(root.querySelector('.data button')?.getAttribute('aria-label')).toBe('< 1 d: 4')
  })

  it('does not draw before the engine has arrived, and not at all when destroyed first', async () => {
    stubMedia(false)
    const { fixture, create, load, chart } = await render(days)
    fixture.componentRef.setInput('spec', series)
    await fixture.whenStable()
    fixture.destroy()
    load()
    await new Promise((resolve) => setTimeout(resolve))
    expect(create).not.toHaveBeenCalled()
    expect(chart.update).not.toHaveBeenCalled()
  })

  it('destroys the chart and stops watching the scheme when destroyed', async () => {
    const queries = stubMedia(false)
    const { fixture, chart, loaded } = await render(days)
    await loaded()
    fixture.destroy()
    expect(chart.destroy).toHaveBeenCalledTimes(1)
    expect(queries['(prefers-color-scheme: dark)'].listeners.size).toBe(0)
    expect(queries['(prefers-reduced-motion: reduce)'].listeners.size).toBe(0)
  })

  it('destroys cleanly when the chart never drew', async () => {
    stubMedia(false)
    const { fixture } = await render(days)
    expect(() => fixture.destroy()).not.toThrow()
  })

  describe('clickable buckets', () => {
    it('emits the link of a clicked bar, and nothing for a bar with none or an unknown one', async () => {
      stubMedia(false)
      const { create, selected, loaded } = await render(buckets)
      await loaded()
      const onClick = create.mock.calls[0][1].options?.onClick as (event: unknown, elements: { index: number }[]) => void
      onClick({}, [{ index: 0 }])
      expect(selected).toEqual([link])
      onClick({}, [{ index: 1 }])
      onClick({}, [{ index: 7 }])
      expect(selected).toHaveLength(1)
    })

    it('reaches the same links from the keyboard: each is a button in the data table', async () => {
      stubMedia(false)
      const { root, selected } = await render(buckets)
      const buttons = root.querySelectorAll<HTMLButtonElement>('.data button')
      expect(buttons).toHaveLength(1)
      buttons[0].click()
      expect(selected).toEqual([link])
    })

    it('has no clickable bar in a chart without links', async () => {
      stubMedia(false)
      const { root, create, selected, loaded } = await render(days)
      await loaded()
      expect(root.querySelectorAll('.data button')).toHaveLength(0)
      ;(create.mock.calls[0][1].options?.onClick as (event: unknown, elements: { index: number }[]) => void)({}, [{ index: 0 }])
      expect(selected).toEqual([])
    })
  })

  it('loads Chart.js through a dynamic import by default, as its own module', async () => {
    TestBed.resetTestingModule()
    const engine = await TestBed.inject(CHART_ENGINE)()
    expect(typeof engine.create).toBe('function')
  })
})
