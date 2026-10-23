import { TestBed } from '@angular/core/testing'
import { FETCH, machineLoad } from '@cockpit/fleet-data'
import { homeStates } from './home-fixtures'
import { MetricsPoller } from '../../metrics/metrics-poller'
import { fleet, modelOf } from './home-testing'
import { MachineStrip, machineTile } from './machine-strip'

const metrics = homeStates().busy.metrics

async function render(samples: boolean) {
  TestBed.resetTestingModule()
  TestBed.configureTestingModule({ providers: [{ provide: FETCH, useValue: async () => new Response('{}', { status: 404 }) }] })
  if (samples) TestBed.inject(MetricsPoller).entries.set(new Map([...metrics].map(([id, answer]) => [id, { metrics: answer, readAt: 0 }])))
  const fixture = TestBed.createComponent(MachineStrip)
  fixture.componentRef.setInput('model', modelOf(fleet()))
  await fixture.whenStable()
  const root: HTMLElement = fixture.nativeElement
  return { fixture, root, tiles: [...root.querySelectorAll<HTMLElement>('.home-tile')] }
}

const text = (element: Element | null | undefined) => (element?.textContent ?? '').replace(/\s+/g, ' ').trim()

describe('machineTile', () => {
  it('says how a machine is reached and the words of the sample behind its load', () => {
    const model = modelOf(fleet())
    const view = (name: string) => model.machines.find((candidate) => candidate.machine.machine === name)!
    const tile = (name: string) => machineTile(model, view(name), machineLoad(metrics.get(view(name).machine.id), model.now))
    expect(tile('mac')).toMatchObject({ name: 'mac', reach: 'local', load: { state: 'free' }, sample: 'local, just now', bars: { cpu: 41, memory: 52 } })
    expect(tile('vm')).toMatchObject({ reach: 'live', load: { state: 'busy' }, sample: 'live, just now' })
    // A cached sample 30 minutes old: the load is unknown, never free, and its age is said.
    expect(tile('old')).toMatchObject({ reach: 'cached 1 d ago', state: 'stale', load: { state: 'not-reported', stale: true }, sample: 'cached, 30 min ago: too old to say' })
    expect(tile('old').bars).toBeUndefined()
  })

  it('has no bars and says so for a machine with no usable sample, never calling it free', () => {
    const model = modelOf(fleet())
    const view = model.machines[0]
    const tile = machineTile(model, view, machineLoad(undefined, model.now))
    expect(tile.load.state).toBe('not-reported')
    expect(tile.bars).toBeUndefined()
    expect(tile.sample).toBe('no usable sample')
    const noTime = machineTile(model, view, { state: 'busy', route: 'none', cpuPercent: 120, memoryPercent: -5 })
    expect(noTime.bars).toEqual({ cpu: 100, memory: 0 })
    expect(noTime.sample).toBe('no samples')
  })
})

describe('MachineStrip', () => {
  it("shows a compact tile per machine: name, reach, load, small CPU and memory bars and the sample's route and age", async () => {
    const { tiles } = await render(true)
    expect(tiles.map((tile) => text(tile.querySelector('.tile-name')))).toEqual(['mac', 'vm', 'old'])
    expect(tiles.map((tile) => text(tile.querySelector('app-state-badge')))).toEqual(
      ['free', 'busy', 'load unknown'].map((word) => expect.stringContaining(word)),
    )
    expect(text(tiles[0].querySelector('.tile-bars'))).toBe('CPU41%Mem52%')
    expect(tiles[1].querySelector('[aria-label="CPU 86 percent"]')).not.toBeNull()
    expect(tiles[0].querySelector('.tile-fill')?.getAttribute('style')).toContain('width: 41%')
    expect(text(tiles[2])).toContain('cached 1 d ago · stale')
    expect(text(tiles[2])).toContain('cached, 30 min ago: too old to say')
    expect(text(tiles[0])).toContain('local, just now')
  })

  it('says "load unknown" and shows no bars until a sample has been read', async () => {
    const { tiles } = await render(false)
    for (const tile of tiles) {
      expect(text(tile.querySelector('app-state-badge'))).toContain('load unknown')
      expect(tile.querySelector('.tile-bars')).toBeNull()
      expect(text(tile)).toContain('no usable sample')
    }
  })
})
