import { Component } from '@angular/core'
import { TestBed } from '@angular/core/testing'
import { provideRouter } from '@angular/router'
import { ClipboardWriter } from '../control/clipboard'
import { PanelCommand, taskDetailLink } from '@cockpit/fleet-data'
import { PanelContent } from './panel-content'

@Component({
  imports: [PanelContent],
  template: `<app-panel-content kind="Worktree" heading="fix-ci"><p class="extra">extra</p><button panelActions>Land</button></app-panel-content>`,
})
class Host {}

const COMMANDS: PanelCommand[] = [
  { title: 'List worktrees', command: { ok: true, text: "wb worktree list 'fix-ci'", needsEdit: false } },
  { title: 'Commit', command: { ok: true, text: 'wb pr create <message>', needsEdit: true, label: 'run on mac' } },
  { title: 'Only edit', command: { ok: true, text: 'wb x <y>', needsEdit: true } },
  { title: 'Only label', command: { ok: true, text: 'wb z', needsEdit: false, label: 'run on mac' } },
  { title: 'Refused', command: { ok: false, reason: 'the name starts with a dash' } },
]

function render(inputs: Record<string, unknown>) {
  TestBed.resetTestingModule()
  TestBed.configureTestingModule({ providers: [provideRouter([])] })
  const fixture = TestBed.createComponent(PanelContent)
  fixture.componentRef.setInput('kind', 'Worktree')
  fixture.componentRef.setInput('heading', 'fix-ci')
  for (const [name, value] of Object.entries(inputs)) fixture.componentRef.setInput(name, value)
  return fixture.whenStable().then(() => ({ fixture, root: fixture.nativeElement as HTMLElement }))
}

const text = (element: Element | null) => (element?.textContent ?? '').replace(/\s+/g, ' ').trim()

describe('PanelContent', () => {
  // cockpit-views#ac:side-panel-shows-summary-actions-commands-and-raw-data
  it('shows the heading, the facts, the related entities, the action area, the commands and a collapsed Raw data block, and no hover card or chevron', async () => {
    const { root } = await render({
      facts: [
        { label: 'Task', text: 'fix-ci', link: taskDetailLink('fix-ci'), copy: true },
        { label: 'Branch', text: 'task/fix-ci', title: 'the branch' },
        { label: 'Last activity', time: Date.parse('2026-10-01T10:00:00Z'), text: '—' },
        { label: 'Source', text: 'local', muted: true },
        { label: 'Plain', text: '' },
      ],
      related: [
        { title: 'Pull requests', items: [{ text: '#1 open', href: 'https://github.com/a/b/pull/1' }, { text: 'one', link: taskDetailLink('t') }, { text: 'plain', title: 'hint' }] },
        { title: 'Agents', items: [] },
      ],
      commands: COMMANDS,
      raw: [{ id: 'w1' }],
    })
    expect(text(root.querySelector('h2'))).toBe('fix-ci')
    expect(text(root.querySelector('.kind'))).toBe('Worktree')
    expect([...root.querySelectorAll('dt')].map(text)).toEqual(['Task', 'Branch', 'Last activity', 'Source', 'Plain'])
    expect(root.querySelector('dd a')?.getAttribute('href')).toBe('/tasks/detail?task=fix-ci')
    expect(root.querySelectorAll('dd app-copy-icon')).toHaveLength(1)
    expect(root.querySelector('dd[title="the branch"]')).not.toBeNull()
    expect(root.querySelectorAll('dd')[3].classList.contains('muted')).toBe(true)
    expect(text(root.querySelectorAll('dd')[2])).toMatch(/ ago$/)
    const external = root.querySelector('a[target="_blank"]') as HTMLAnchorElement
    expect(external.getAttribute('rel')).toBe('noopener noreferrer')
    expect(external.getAttribute('href')).toBe('https://github.com/a/b/pull/1')
    expect(text(root.querySelector('[aria-label="Agents"]'))).toContain('None')
    expect(root.querySelector('section.actions')).not.toBeNull()
    // The "Copy command" entries are the control surface's list.
    expect(root.querySelectorAll('app-copy-command-list li')).toHaveLength(5)
    expect(text(root.querySelector('app-copy-command-list .title'))).toBe('List worktrees')
    expect(text(root.querySelector('app-copy-command-list code'))).toBe("wb worktree list 'fix-ci'")
    expect(text(root.querySelector('app-copy-command-list .refused'))).toContain('the name starts with a dash')
    const details = root.querySelector('details') as HTMLDetailsElement
    expect(details.open).toBe(false)
    expect(root.querySelector('pre')).toBeNull()
    // No hover card, tooltip role or row-expansion chevron anywhere.
    expect(root.querySelector('[role=tooltip], .count-card, [aria-expanded]')).toBeNull()
  })

  it('renders the entries exactly as the read model sent them once Raw data is opened, and not before', async () => {
    const entry = { id: 'w1', task: 'fix-ci', route: 'local' }
    const { fixture, root } = await render({ raw: [entry] })
    const details = root.querySelector('details') as HTMLDetailsElement
    details.open = true
    details.dispatchEvent(new Event('toggle'))
    await fixture.whenStable()
    expect(JSON.parse(root.querySelector('pre')?.textContent as string)).toEqual([entry])
    details.open = false
    details.dispatchEvent(new Event('toggle'))
    await fixture.whenStable()
    expect(root.querySelector('pre')).toBeNull()
  })

  it('offers copy icons that copy the full value, on the heading and a fact, and says Copied once, in the panel\'s own live region', async () => {
    const copy = vi.fn(async () => true)
    TestBed.resetTestingModule()
    TestBed.configureTestingModule({ providers: [provideRouter([]), { provide: ClipboardWriter, useValue: { copy } }] })
    const fixture = TestBed.createComponent(PanelContent)
    fixture.componentRef.setInput('kind', 'Branch')
    fixture.componentRef.setInput('heading', 'a-long-branch-name')
    fixture.componentRef.setInput('copyHeading', true)
    fixture.componentRef.setInput('facts', [{ label: 'Session', text: 'sess-1234567890', copy: true }])
    await fixture.whenStable()
    const icons = [...fixture.nativeElement.querySelectorAll('app-copy-icon button')] as HTMLButtonElement[]
    expect(icons.map((button) => button.getAttribute('aria-label'))).toEqual(['Copy branch name', 'Copy session'])
    for (const icon of icons) icon.click()
    await vi.waitFor(() => expect(copy).toHaveBeenCalledTimes(2))
    expect(copy.mock.calls.map((call) => call[0])).toEqual(['a-long-branch-name', 'sess-1234567890'])
    await vi.waitFor(() => expect(fixture.nativeElement.querySelector('article > [role=status]').textContent).toBe('Copied'))
  })

  it('shows a raw entry that looks like HTML as text, never as markup', async () => {
    const hostile = { id: 'w1', task: '<img src=x onerror=alert(1)>', note: '</pre><script>alert(2)</script>' }
    const { fixture, root } = await render({ raw: [hostile], facts: [{ label: 'Task', text: hostile.task }], heading: hostile.task })
    const details = root.querySelector('details') as HTMLDetailsElement
    details.open = true
    details.dispatchEvent(new Event('toggle'))
    await fixture.whenStable()
    expect(root.querySelector('img, script')).toBeNull()
    expect(root.querySelector('pre')?.textContent).toContain('<img src=x onerror=alert(1)>')
    expect(root.querySelector('pre')?.textContent).toContain('</pre><script>alert(2)</script>')
    expect(text(root.querySelector('h2'))).toBe(hostile.task)
  })

  it('is the page when asked, with the same content', async () => {
    const side = await render({ facts: [{ label: 'A', text: 'b' }] })
    const page = await render({ facts: [{ label: 'A', text: 'b' }], page: true })
    expect(side.root.querySelector('.content')?.classList.contains('page')).toBe(false)
    expect(page.root.querySelector('.content')?.classList.contains('page')).toBe(true)
    expect(text(page.root.querySelector('dl'))).toBe(text(side.root.querySelector('dl')))
  })

  it('projects the page\'s own content and the action area', async () => {
    const fixture = TestBed.createComponent(Host)
    await fixture.whenStable()
    const root = fixture.nativeElement as HTMLElement
    expect(text(root.querySelector('.extra'))).toBe('extra')
    expect(text(root.querySelector('section.actions button'))).toBe('Land')
  })

  it('has no commands section when there are none', async () => {
    const { root } = await render({})
    expect(root.querySelector('[aria-label="Copy command"]')).toBeNull()
  })
})
