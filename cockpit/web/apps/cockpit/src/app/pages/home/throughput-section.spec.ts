import { DOCUMENT } from '@angular/common'
import { TestBed } from '@angular/core/testing'
import { By } from '@angular/platform-browser'
import { provideRouter } from '@angular/router'
import type { FleetDocument } from '@cockpit/fleet-data'
import { CHART_ENGINE } from '@cockpit/ui/chart'
import { FIXED_CLOCK, fleet, modelOf } from './home-testing'
import { LazyMount } from './lazy-mount'
import { RELOAD_PAGE, ThroughputSection, hasThroughput, throughputState } from './throughput-section'

const withoutBlock = () => fleet('no-throughput')
const emptyBlock = (): FleetDocument => ({ ...fleet(), throughput: { window_days: 30, per_day: [], slowest: [] } })

async function render(document = fleet(), warming = false, reload = vi.fn()) {
  TestBed.resetTestingModule()
  TestBed.configureTestingModule({ providers: [{ provide: RELOAD_PAGE, useValue: reload }, FIXED_CLOCK, provideRouter([]), { provide: CHART_ENGINE, useValue: async () => ({ create: () => ({ update: vi.fn(), destroy: vi.fn() }) }) }] })
  const fixture = TestBed.createComponent(ThroughputSection)
  const show = async (next: FleetDocument, nextWarming: boolean) => {
    fixture.componentRef.setInput('model', modelOf(next))
    fixture.componentRef.setInput('warming', nextWarming)
    await fixture.whenStable()
  }
  await show(document, warming)
  const root = fixture.nativeElement as HTMLElement
  const slot = () => root.querySelector('.throughput-slot')
  const arrived = () =>
    vi.waitFor(async () => {
      await fixture.whenStable()
      expect(root.querySelectorAll('.throughput-slot app-chart')).toHaveLength(2)
    })
  return { fixture, root, slot, show, arrived }
}

describe('throughputState', () => {
  it('is pending while the daemon scans, whatever the document says, and otherwise charts when there is anything to chart and none when there is not', () => {
    expect(throughputState(modelOf(fleet()), true)).toBe('pending')
    expect(throughputState(modelOf(withoutBlock()), true)).toBe('pending')
    expect(throughputState(modelOf(fleet()), false)).toBe('charts')
    expect(throughputState(modelOf(withoutBlock()), false)).toBe('none')
    expect(throughputState(modelOf(emptyBlock()), false)).toBe('none')
  })

  it('counts a block with a day, or with a slowest task, and no other, as throughput', () => {
    expect(hasThroughput(modelOf(fleet()))).toBe(true)
    expect(hasThroughput(modelOf(withoutBlock()))).toBe(false)
    expect(hasThroughput(modelOf(emptyBlock()))).toBe(false)
    const block = fleet().throughput!
    expect(hasThroughput(modelOf({ ...fleet(), throughput: { ...block, per_day: [], slowest: block.slowest } }))).toBe(true)
    expect(hasThroughput(modelOf({ ...fleet(), throughput: { ...block, per_day: block.per_day, slowest: [] } }))).toBe(true)
  })
})

describe('ThroughputSection', () => {
  it('is a heading and a slot with a hidden status and a skeleton, and a busy section, while the daemon scans', async () => {
    const { root, slot } = await render(withoutBlock(), true)
    expect(root.querySelector('h2')?.textContent?.trim()).toBe('Throughput')
    expect(root.querySelector('section')?.getAttribute('aria-busy')).toBe('true')
    expect(slot()?.getAttribute('data-state')).toBe('pending')
    expect(root.querySelector('[role=status].visually-hidden')?.textContent).toBe('Loading throughput charts')
    expect(root.querySelector('.throughput-skeleton')?.getAttribute('aria-hidden')).toBe('true')
    expect(root.querySelector('app-lazy-mount')).toBeNull()
  })

  it('shows the two charts from their lazy chunk, in the slot, in a section that is not busy', async () => {
    const { root, slot, arrived } = await render()
    expect(root.querySelector('section')?.getAttribute('aria-busy')).toBeNull()
    expect(root.querySelector('section')?.getAttribute('aria-labelledby')).toBe(root.querySelector('h2')?.id)
    expect(slot()?.getAttribute('data-state')).toBe('charts')
    await arrived()
    expect(root.querySelector('.home-calm')).toBeNull()
  })

  it.each([
    ['has no throughput block', withoutBlock],
    ['has a block with nothing in it', emptyBlock],
  ])('says in real text, centred in the slot, that there is nothing to chart when a complete document %s', async (_name, document) => {
    const { root, slot } = await render(document())
    expect(slot()?.getAttribute('data-state')).toBe('none')
    expect(root.querySelector('.throughput-note')?.textContent).toContain('No charts: the daemon reports no throughput')
    expect(root.querySelector('app-lazy-mount')).toBeNull()
  })

  it('keeps one slot, the same element, through pending, none, charts and none again', async () => {
    const { root, slot, show, arrived } = await render(withoutBlock(), true)
    const first = slot()
    await show(withoutBlock(), false)
    expect(slot()?.getAttribute('data-state')).toBe('none')
    await show(fleet(), false)
    expect(slot()?.getAttribute('data-state')).toBe('charts')
    await arrived()
    await show(withoutBlock(), false)
    expect(slot()?.getAttribute('data-state')).toBe('none')
    expect(root.querySelectorAll('.throughput-slot')).toHaveLength(1)
    expect(slot()).toBe(first)
  })

  it('says the charts are unavailable when their chunk cannot be fetched, and reloads the page on Retry, the one way to ask for it again', async () => {
    const reload = vi.fn()
    const { fixture, root, slot } = await render(fleet(), false, reload)
    fixture.debugElement.query(By.directive(LazyMount)).componentInstance.loadFailed.emit()
    await fixture.whenStable()
    expect(slot()?.getAttribute('data-state')).toBe('failed')
    expect(root.querySelector('.throughput-note')?.textContent).toContain('Charts unavailable.')
    expect(root.querySelector('app-lazy-mount')).toBeNull()
    expect(reload).not.toHaveBeenCalled()
    root.querySelector<HTMLButtonElement>('.throughput-note button')?.click()
    expect(reload).toHaveBeenCalledTimes(1)
  })

  it('forgets a failure once the slot holds something else', async () => {
    const { fixture, slot, show, arrived } = await render()
    fixture.debugElement.query(By.directive(LazyMount)).componentInstance.loadFailed.emit()
    await fixture.whenStable()
    await show(withoutBlock(), false)
    expect(slot()?.getAttribute('data-state')).toBe('none')
    await show(fleet(), false)
    expect(slot()?.getAttribute('data-state')).toBe('charts')
    await arrived()
  })
})

describe('RELOAD_PAGE', () => {
  it('reloads the window of the document, and does nothing without one', () => {
    const reload = vi.fn()
    TestBed.resetTestingModule()
    TestBed.configureTestingModule({ providers: [{ provide: DOCUMENT, useValue: { defaultView: { location: { reload } } } }] })
    TestBed.inject(RELOAD_PAGE)()
    expect(reload).toHaveBeenCalledTimes(1)
    TestBed.resetTestingModule()
    TestBed.configureTestingModule({ providers: [{ provide: DOCUMENT, useValue: { defaultView: null } }] })
    expect(() => TestBed.inject(RELOAD_PAGE)()).not.toThrow()
  })
})
