import { TestBed } from '@angular/core/testing'
import { Router, provideRouter } from '@angular/router'
import { FETCH, FleetStore, hrefOf } from '@cockpit/fleet-data'
import { agent, fleetDocument, repository, worktree } from '@cockpit/fleet-data/testing'
import { ShellState } from '../shell/shell-state'
import { CommandPalette, revealSelected, scrollIntoList } from './command-palette'
import { RECENTS_KEY, RECENTS_STORAGE } from './recents'

class MemoryStorage {
  data = new Map<string, string>()
  getItem = (key: string) => this.data.get(key) ?? null
  setItem = (key: string, value: string) => void this.data.set(key, value)
}

function fleet() {
  const repositories = Array.from({ length: 10 }, (_, index) => repository(`g${index}`, 'alpha', { name: `acme/go-${index}`, worktree_count: 0, active_agent_count: 0 }))
  return fleetDocument({
    repositories,
    worktrees: [{ ...worktree('w1', 'g0', 'alpha'), task: 'go-live', branch: 'task/go-live' }],
    agents: [agent('a1', 'g0', 'running', { runtime: 'golang' })],
  })
}

describe('CommandPalette', () => {
  const fetcher = vi.fn()
  let storage: MemoryStorage
  let navigate: ReturnType<typeof vi.spyOn>

  async function open(stored?: unknown[]) {
    storage = new MemoryStorage()
    if (stored) storage.setItem(RECENTS_KEY, JSON.stringify(stored))
    TestBed.configureTestingModule({
      providers: [provideRouter([]), { provide: FETCH, useValue: fetcher }, { provide: RECENTS_STORAGE, useValue: storage }],
    })
    const store = TestBed.inject(FleetStore)
    store.loaded.set(true)
    store.document.set(fleet())
    store.now.set(Date.parse('2026-10-01T10:05:00Z'))
    navigate = vi.spyOn(TestBed.inject(Router), 'navigateByUrl').mockResolvedValue(true)
    const shell = TestBed.inject(ShellState)
    const fixture = TestBed.createComponent(CommandPalette)
    document.body.append(fixture.nativeElement)
    await fixture.whenStable()
    shell.openPalette()
    await fixture.whenStable()
    const root: HTMLElement = fixture.nativeElement
    const input = root.querySelector('input') as HTMLInputElement
    const type = async (text: string) => {
      input.value = text
      input.dispatchEvent(new Event('input'))
      await fixture.whenStable()
    }
    const press = async (key: string) => {
      const event = new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true })
      input.dispatchEvent(event)
      await fixture.whenStable()
      return event
    }
    const options = () => [...root.querySelectorAll('[role="option"]')]
    const selected = () => root.querySelector('[role="option"][aria-selected="true"]')
    return { fixture, root, input, type, press, options, selected, shell }
  }

  afterEach(() => {
    fetcher.mockReset()
    document.body.innerHTML = ''
  })

  // cockpit-views#ac:palette-groups-results
  it('opens at once with the focus in the input and no request and no navigation', async () => {
    const { root, input } = await open()
    expect(root.querySelector('[role="dialog"]')).not.toBeNull()
    expect(document.activeElement).toBe(input)
    expect(fetcher).not.toHaveBeenCalled()
    expect(navigate).not.toHaveBeenCalled()
  })

  it('groups the results by kind, with at most 8 each, and opens the highlighted one with Enter after two arrows down', async () => {
    const { root, type, press, options, selected, shell } = await open()
    await type('go')
    const titles = [...root.querySelectorAll('.group-title')].map((title) => title.textContent)
    expect(titles).toEqual(['Tasks', 'Repositories', 'Worktrees', 'Branches', 'Agents'])
    const repositories = root.querySelectorAll('[role="group"]')[1]
    expect(repositories.querySelectorAll('[role="option"]')).toHaveLength(8)
    expect(repositories.querySelector('.more')?.textContent).toContain('2 more')
    expect(selected()).toBe(options()[0])

    await press('ArrowDown')
    await press('ArrowDown')
    expect(selected()).toBe(options()[2])
    expect(root.querySelector('input')?.getAttribute('aria-activedescendant')).toBe('palette-option-2')
    const event = await press('Enter')
    expect(event.defaultPrevented).toBe(true)
    expect(navigate).toHaveBeenCalledTimes(1)
    expect(navigate.mock.calls[0][0]).toMatch(/^\/repositories\/github\.com\/acme\/go-\d$/)
    expect(shell.paletteOpen()).toBe(false)
  })

  it('wraps the highlight around the ends of the list', async () => {
    const { type, press, options, selected } = await open()
    await type('go-live')
    expect(options()).toHaveLength(3)
    await press('ArrowUp')
    expect(selected()).toBe(options()[2])
    await press('ArrowDown')
    expect(selected()).toBe(options()[0])
    // Another key does nothing.
    expect((await press('a')).defaultPrevented).toBe(false)
  })

  it('moves the highlight with the mouse and opens a result with a click', async () => {
    const { fixture, type, options, selected } = await open()
    await type('go-live')
    options()[1].dispatchEvent(new MouseEvent('mousemove', { bubbles: true }))
    await fixture.whenStable()
    expect(selected()).toBe(options()[1])
    ;(options()[1] as HTMLElement).click()
    expect(navigate).toHaveBeenCalledWith(hrefOf({ path: '/worktrees/w1', query: {} }))
  })

  it('says when nothing matches, and invites typing while the input is empty', async () => {
    const { root, type, press, selected } = await open()
    expect(root.querySelector('.empty')?.textContent).toContain('Type to search')
    expect(root.querySelector('.tip')).not.toBeNull()
    await type('zzzz')
    expect(root.querySelector('.empty')?.textContent).toContain('Nothing matches “zzzz”')
    expect(root.querySelector('.tip')).toBeNull()
    expect(root.querySelector('input')?.getAttribute('aria-activedescendant')).toBeNull()
    expect(selected()).toBeNull()
    // No result to move through or open.
    expect((await press('ArrowDown')).defaultPrevented).toBe(true)
    await press('Enter')
    expect(navigate).not.toHaveBeenCalled()
  })

  it('remembers what was opened and shows it first while the input is empty', async () => {
    const { fixture, root, type, options, shell } = await open()
    await type('golang')
    ;(options()[0] as HTMLElement).click()
    expect(JSON.parse(storage.data.get(RECENTS_KEY) as string)[0].label).toBe('golang session s-a1')
    await fixture.whenStable()
    shell.openPalette()
    await fixture.whenStable()
    expect(root.querySelector('.group-title')?.textContent).toBe('Recent')
    expect(options().map((option) => option.querySelector('.label')?.textContent)).toEqual(['golang session s-a1'])
  })

  it('lists the recents of the browser when the input is empty, and opens one', async () => {
    const { root, options } = await open([{ id: 'task:x', kind: 'task', label: 'x', detail: 'working', link: { path: '/tasks/detail', query: { task: 'x' } } }])
    expect(root.querySelector('.group-title')?.textContent).toBe('Recent')
    ;(options()[0] as HTMLElement).click()
    expect(navigate).toHaveBeenCalledWith('/tasks/detail?task=x')
  })

  it('closes from its backdrop and forgets the typed text', async () => {
    const { fixture, root, type, shell } = await open()
    await type('go')
    ;(root.querySelector('.overlay-backdrop') as HTMLElement).click()
    expect(shell.paletteOpen()).toBe(false)
    await fixture.whenStable()
    shell.openPalette()
    await fixture.whenStable()
    expect((root.querySelector('input') as HTMLInputElement).value).toBe('')
  })
})

describe('scrollIntoList', () => {
  it('scrolls up to an item above the view, down to one below it, and leaves one inside', () => {
    const list = { scrollTop: 100, clientHeight: 200 }
    scrollIntoList(list, { offsetTop: 40, offsetHeight: 36 })
    expect(list.scrollTop).toBe(40)
    scrollIntoList(list, { offsetTop: 300, offsetHeight: 36 })
    expect(list.scrollTop).toBe(136)
    scrollIntoList(list, { offsetTop: 150, offsetHeight: 36 })
    expect(list.scrollTop).toBe(136)
  })
})

describe('revealSelected', () => {
  it('scrolls to the selected option, and does nothing without a list or without a selection', () => {
    revealSelected(undefined)
    const list = document.createElement('div')
    revealSelected(list)
    const item = document.createElement('div')
    item.setAttribute('aria-selected', 'true')
    Object.defineProperty(item, 'offsetTop', { value: 500 })
    Object.defineProperty(item, 'offsetHeight', { value: 36 })
    Object.defineProperty(list, 'clientHeight', { value: 100 })
    list.append(item)
    revealSelected(list)
    expect(list.scrollTop).toBe(436)
  })
})
