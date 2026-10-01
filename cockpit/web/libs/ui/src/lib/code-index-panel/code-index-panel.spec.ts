import { TestBed } from '@angular/core/testing'
import { CodeIndex, Entry } from '@cockpit/fleet-data'
import { CodeIndexPanel } from './code-index-panel'

const NOW = Date.parse('2026-10-01T10:05:00Z')
const local: Entry = { id: 'r1', machine: 'alpha', machine_id: 'mach-alpha', route: 'local' }
const cached: Entry = { ...local, route: 'cached' }

async function render(entry: Entry, states: CodeIndex[] | undefined, provider: string | undefined) {
  const fixture = TestBed.createComponent(CodeIndexPanel)
  fixture.componentRef.setInput('entry', entry)
  fixture.componentRef.setInput('states', states)
  fixture.componentRef.setInput('provider', provider)
  fixture.componentRef.setInput('now', NOW)
  await fixture.whenStable()
  return fixture.nativeElement as HTMLElement
}

const text = (element: Element | null) => (element?.textContent as string).replace(/\s+/g, ' ').trim()

const indexed: CodeIndex[] = [
  {
    indexer: 'codegrapher',
    state: 'fresh',
    receipt_at: '2026-10-01T10:00:00Z',
    statistics: {
      indexed: true,
      files: 12,
      symbols: 40,
      edges: 90,
      kinds: [
        { kind: 'function', count: 30 },
        { kind: 'struct', count: 10 },
      ],
    },
  },
]

// cockpit#ac:code-index-panel
describe('CodeIndexPanel', () => {
  it('shows the freshness, the three totals and the symbols by kind of an indexed checkout', async () => {
    const root = await render(local, indexed, 'codegrapher')
    expect(text(root.querySelector('.panel-freshness'))).toBe('Freshness: fresh 5 min ago')
    expect([...root.querySelectorAll('.totals div')].map((item) => text(item))).toEqual(['Files12', 'Symbols40', 'Edges90'])
    expect([...root.querySelectorAll('.kinds li')].map((item) => text(item))).toEqual(['function30', 'struct10'])
  })

  it('shows the totals of an index with no symbol kinds without an empty breakdown', async () => {
    const states: CodeIndex[] = [{ indexer: 'codegrapher', state: 'fresh', statistics: { indexed: true, files: 1, symbols: 0, edges: 0, kinds: [] } }]
    const root = await render(local, states, 'codegrapher')
    expect(text(root.querySelector('.totals'))).toBe('Files1Symbols0Edges0')
    expect(root.querySelector('.kinds')).toBeNull()
  })

  it('says not indexed when the provider found no index, or none was read', async () => {
    const none: CodeIndex[] = [{ indexer: 'codegrapher', state: 'never', statistics: { indexed: false, files: 0, symbols: 0, edges: 0, kinds: [] } }]
    expect(text((await render(local, none, 'codegrapher')).querySelector('.panel-note'))).toBe('Not indexed.')
    expect(text((await render(local, undefined, 'codegrapher')).querySelector('.panel-note'))).toBe('Not indexed.')
  })

  it('says no provider is configured, with the key that configures one', async () => {
    const root = await render(local, indexed, undefined)
    expect(text(root.querySelector('.panel-note'))).toContain('No code-index provider is configured.')
    expect(text(root.querySelector('code'))).toBe('cockpit.code_index_provider')
    expect(root.querySelector('.totals')).toBeNull()
  })

  it('says the statistics are not known for a checkout on another machine', async () => {
    const root = await render(cached, undefined, 'codegrapher')
    expect(text(root.querySelector('.panel-note'))).toContain('another machine')
  })

  it('names the short failure code of a provider that could not answer, and nothing else', async () => {
    const failed: CodeIndex[] = [{ indexer: 'codegrapher', state: 'fresh', statistics: { indexed: false, files: 0, symbols: 0, edges: 0, kinds: [], error: 'provider_timeout' } }]
    const root = await render(local, failed, 'codegrapher')
    expect(text(root.querySelector('.panel-note'))).toBe('The code-index provider could not read this index (provider_timeout).')
  })

  it('carries no count component: the totals are statistics, not counts of entities', async () => {
    const root = await render(local, indexed, 'codegrapher')
    expect(root.querySelector('app-count')).toBeNull()
    expect(root.querySelector('a')).toBeNull()
  })
})
