import { TestBed } from '@angular/core/testing'
import { ActionsResponse, RegistryAction } from '@cockpit/fleet-data'
import { fakeControlFetch, registryAction } from '@cockpit/fleet-data/testing'
import { ActionActivation, ActionSlot, OWNER_ONLY_EXPLANATION, SlotCopy, disabledReason, isDirect } from './action-slot'
import { ClipboardWriter } from './clipboard'

const REGISTRY: Record<string, RegistryAction[]> = {
  'worktree:wt-1': [
    registryAction('pr.create', 'Open pull request', { safety: 'guarded', capability: 'pr.create' }),
    registryAction('worktree.discard', 'Discard worktree', { safety: 'destructive', applicable: false, reason: '2 commits are not on the remote', capability: 'worktree.discard' }),
    registryAction('test.extra', 'An action nobody has heard of', { safety: 'safe' }),
  ],
  'pull_request:pr-1': [registryAction('pr.land', 'Land', { target_types: ['pull_request'], capability: 'pr.land' })],
}

async function actionsFor(target: string, registry: Record<string, RegistryAction[]> | undefined): Promise<RegistryAction[] | undefined> {
  const fetcher = fakeControlFetch({ registry }, async () => new Response('{}', { status: 599 }))
  const response = await fetcher(`/api/v1/cockpit/actions?target=${target}`)
  return response.ok ? ((await response.json()) as ActionsResponse).actions : undefined
}

async function render(actions: readonly RegistryAction[] | undefined, target = 'worktree:wt-1', handled = true) {
  TestBed.resetTestingModule()
  const fixture = TestBed.createComponent(ActionSlot)
  fixture.componentRef.setInput('actions', actions)
  fixture.componentRef.setInput('target', target)
  const activations: ActionActivation[] = []
  if (handled) fixture.componentRef.setInput('run', (activation: ActionActivation) => activations.push(activation))
  await fixture.whenStable()
  const root: HTMLElement = fixture.nativeElement
  const buttons = () => [...root.querySelectorAll<HTMLButtonElement>('.slot > button.action:not(.more)')]
  const trigger = () => root.querySelector<HTMLButtonElement>('.more')
  const menu = () => root.querySelector<HTMLElement>('.menu')
  const items = () => [...root.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')]
  const note = () => root.querySelector('.note')?.textContent
  return { fixture, root, activations, buttons, trigger, menu, items, note }
}

const text = (element: Element | null | undefined) => element?.textContent?.replace(/\s+/g, ' ').trim()
const unclassified = (extra: object): RegistryAction => ({ ...registryAction('x', 'X'), ...extra }) as RegistryAction

describe('ActionSlot', () => {
  afterEach(() => vi.restoreAllMocks())

  // cockpit-views#ac:action-area-renders-the-registry-and-vanishes-without-it
  it('renders exactly what the registry returns: direct buttons, the destructive action under the overflow menu, the disabled one with its reason', async () => {
    const actions = await actionsFor('worktree:wt-1', REGISTRY)
    const { buttons, trigger, items } = await render(actions)
    expect(buttons().map((button) => text(button))).toEqual(['Open pull request', 'An action nobody has heard of'])
    expect(buttons().every((button) => !button.classList.contains('disabled'))).toBe(true)
    expect(trigger()?.getAttribute('aria-haspopup')).toBe('menu')
    expect(items().map((item) => text(item.firstElementChild))).toEqual(['Discard worktree'])
    expect(items()[0].getAttribute('aria-disabled')).toBe('true')
    expect(text(items()[0].querySelector('.why'))).toBe('2 commits are not on the remote')
    expect(text(items()[0])).toBe('Discard worktree 2 commits are not on the remote')
  })

  it('renders a slot per target: a pull request has its landing action, a task has none', async () => {
    expect((await render(await actionsFor('pull_request:pr-1', REGISTRY), 'pull_request:pr-1')).buttons().map((button) => text(button))).toEqual(['Land'])
    const task = await render(await actionsFor('task:fix-ci', REGISTRY), 'task:fix-ci')
    expect(task.root.children).toHaveLength(0)
  })

  it('is absent entirely, with no placeholder and no box, when the registry route is absent or returns nothing', async () => {
    expect(await actionsFor('worktree:wt-1', undefined)).toBeUndefined()
    for (const actions of [undefined, []]) {
      const { root, fixture } = await render(actions)
      expect(root.children).toHaveLength(0)
      expect(root.textContent).toBe('')
      fixture.componentRef.setInput('actions', REGISTRY['pull_request:pr-1'])
      await fixture.whenStable()
      expect(root.querySelector('.slot')).not.toBeNull()
    }
  })

  it('has no overflow control when every action is direct', async () => {
    const { trigger, menu } = await render(REGISTRY['pull_request:pr-1'])
    expect(trigger()).toBeNull()
    expect(menu()).toBeNull()
  })

  // cockpit-views#ac:action-area-renders-the-registry-and-vanishes-without-it
  describe('without a handler', () => {
    const copy: SlotCopy = { build: async () => ({ ok: true, text: "wb pr land 'o/r#1'", needsEdit: false }), label: 'Copy wb pr land: o/r#1' }

    async function renderUnhandled(actions: readonly RegistryAction[] | undefined, slotCopy: SlotCopy | undefined) {
      const clipboard = vi.fn().mockResolvedValue(true)
      TestBed.resetTestingModule()
      TestBed.configureTestingModule({ providers: [{ provide: ClipboardWriter, useValue: { copy: clipboard } }] })
      const fixture = TestBed.createComponent(ActionSlot)
      fixture.componentRef.setInput('actions', actions)
      fixture.componentRef.setInput('target', 'pull_request:pr-1')
      if (slotCopy !== undefined) fixture.componentRef.setInput('copy', slotCopy)
      await fixture.whenStable()
      return { fixture, root: fixture.nativeElement as HTMLElement, clipboard }
    }

    it('renders the Copy control, never the registry\'s button, when the registry answered and no handler is bound', async () => {
      const actions = await actionsFor('pull_request:pr-1', REGISTRY)
      expect(actions?.length).toBeGreaterThan(0)
      const { root, clipboard } = await renderUnhandled(actions, copy)
      expect(root.querySelector('.slot')).toBeNull()
      expect(root.querySelector('.action')).toBeNull()
      const buttons = [...root.querySelectorAll('button')]
      expect(buttons.map((button) => text(button))).toEqual(['Copy'])
      expect(buttons[0].getAttribute('aria-label')).toBe('Copy wb pr land: o/r#1')
      buttons[0].click()
      await vi.waitFor(() => expect(clipboard).toHaveBeenCalledWith("wb pr land 'o/r#1'"))
    })

    it('says "Copy template" for a command with a part to edit, and is quiet when asked', async () => {
      const { root } = await renderUnhandled(undefined, { ...copy, template: true, quiet: true })
      const button = root.querySelector('button') as HTMLButtonElement
      expect(text(button)).toBe('Copy template')
      expect(button.classList.contains('quiet')).toBe(true)
      expect(button.classList.contains('icon-only')).toBe(true)
    })

    it('renders nothing at all when it has neither a handler nor a command to copy', async () => {
      const { root } = await renderUnhandled(await actionsFor('pull_request:pr-1', REGISTRY), undefined)
      expect(root.children).toHaveLength(0)
    })

    it('draws the registry\'s buttons once a handler is bound, and the Copy control is gone', async () => {
      const { fixture, root } = await renderUnhandled(await actionsFor('pull_request:pr-1', REGISTRY), copy)
      expect(root.querySelector('.slot')).toBeNull()
      fixture.componentRef.setInput('run', () => undefined)
      await fixture.whenStable()
      expect(root.querySelector('.slot')).not.toBeNull()
      expect(root.querySelector('app-lazy-copy')).toBeNull()
    })
  })

  // Fail closed: only the two known safe classes are prominent buttons.
  it('puts an action of an unknown or missing safety class under the overflow menu, never in a direct button', async () => {
    const odd = [
      registryAction('a', 'Known safe', { safety: 'safe' }),
      unclassified({ id: 'b', title: 'Unknown class', safety: 'dangerous-new-class' }),
      unclassified({ id: 'c', title: 'No class', safety: undefined }),
      unclassified({ id: 'd', title: 'Null class', safety: null }),
      registryAction('e', 'Known destructive', { safety: 'destructive' }),
    ]
    const { buttons, items } = await render(odd)
    expect(buttons().map((button) => text(button))).toEqual(['Known safe'])
    expect(items().map((item) => text(item.firstElementChild))).toEqual(['Unknown class', 'No class', 'Null class', 'Known destructive'])
    expect(isDirect(odd[0])).toBe(true)
    expect(isDirect(registryAction('g', 'G', { safety: 'guarded' }))).toBe(true)
    expect(odd.slice(1).some(isDirect)).toBe(false)
  })

  it('disables a not-applicable action with the registry reason read once, a default one when it gives none, and keeps it focusable', async () => {
    const { buttons } = await render([
      registryAction('a', 'First', { applicable: false, reason: 'Nothing to commit' }),
      registryAction('b', 'Second', { applicable: false }),
      registryAction('c', 'Third'),
    ])
    const [first, second, third] = buttons()
    expect(first.getAttribute('aria-disabled')).toBe('true')
    expect(first.hasAttribute('disabled')).toBe(false)
    // Read once: the description, and no title that a screen reader would read again.
    expect(first.hasAttribute('title')).toBe(false)
    expect(first.nextElementSibling?.textContent).toBe('Nothing to commit')
    expect(first.getAttribute('aria-describedby')).toBe(first.nextElementSibling?.id)
    expect(second.nextElementSibling?.textContent).toBe('Not available now')
    expect(third.getAttribute('aria-disabled')).toBeNull()
    expect(third.getAttribute('aria-describedby')).toBeNull()
  })

  it('shows the reason in words on hover, on focus and on a tap, and takes it away on leaving', async () => {
    const { fixture, buttons, note } = await render([registryAction('a', 'First', { applicable: false, reason: 'Nothing to commit' }), registryAction('c', 'Third')])
    const [first, third] = buttons()
    const settle = () => fixture.whenStable()
    expect(note()).toBeUndefined()
    first.dispatchEvent(new MouseEvent('mouseenter'))
    await settle()
    expect(note()).toBe('First: Nothing to commit')
    first.dispatchEvent(new MouseEvent('mouseleave'))
    await settle()
    expect(note()).toBeUndefined()
    first.dispatchEvent(new FocusEvent('focus'))
    await settle()
    expect(note()).toBe('First: Nothing to commit')
    first.dispatchEvent(new FocusEvent('blur'))
    await settle()
    expect(note()).toBeUndefined()
    first.click()
    await settle()
    expect(note()).toBe('First: Nothing to commit')
    // An enabled action has nothing to explain.
    third.dispatchEvent(new MouseEvent('mouseenter'))
    await settle()
    expect(note()).toBeUndefined()
  })

  // cockpit-views#ac:owner-gating-is-one-affordance
  it('shows every action the caller may not run disabled with the same single explanation, and no sign-in message of its own', async () => {
    const { buttons, items, root } = await render([
      registryAction('a', 'First', { permitted: false }),
      registryAction('b', 'Second', { permitted: false, applicable: false, reason: 'Nothing to commit' }),
      registryAction('c', 'Third', { permitted: false, safety: 'destructive' }),
    ])
    const reasons = [...buttons().map((button) => button.nextElementSibling?.textContent), ...items().map((item) => item.querySelector('.why')?.textContent?.trim())]
    expect(reasons).toEqual([OWNER_ONLY_EXPLANATION, OWNER_ONLY_EXPLANATION, OWNER_ONLY_EXPLANATION])
    expect(root.textContent).not.toMatch(/sign in|wb cockpit/i)
    expect(disabledReason(registryAction('x', 'X', { permitted: false }))).toBe(OWNER_ONLY_EXPLANATION)
    expect(disabledReason(registryAction('x', 'X'))).toBeUndefined()
  })

  // cockpit-views#ac:intent-to-done-budgets-hold
  it('opens a preview by emitting at once: no request and no route navigation, and nothing for a disabled action', async () => {
    const fetcher = vi.spyOn(globalThis, 'fetch')
    const { buttons, activations, items } = await render(await actionsFor('worktree:wt-1', REGISTRY))
    const before = window.location.href
    buttons()[0].click()
    expect(activations).toEqual([{ action: REGISTRY['worktree:wt-1'][0], target: 'worktree:wt-1' }])
    items()[0].click()
    expect(activations).toHaveLength(1)
    expect(fetcher).not.toHaveBeenCalled()
    expect(window.location.href).toBe(before)
  })

  describe('the overflow menu', () => {
    const DESTRUCTIVE = [registryAction('d1', 'Discard', { safety: 'destructive' }), registryAction('d2', 'Delete branch', { safety: 'destructive' }), registryAction('d3', 'Third', { safety: 'destructive' })]

    function place(trigger: HTMLElement | null, rect: Partial<DOMRect>, innerWidth = 1024, innerHeight = 768) {
      vi.spyOn(trigger as HTMLElement, 'getBoundingClientRect').mockReturnValue({ top: 0, bottom: 0, left: 0, right: 0, ...rect } as DOMRect)
      vi.stubGlobal('innerWidth', innerWidth)
      vi.stubGlobal('innerHeight', innerHeight)
    }

    afterEach(() => {
      vi.unstubAllGlobals()
      document.body.innerHTML = ''
    })

    async function opened() {
      const parts = await render(DESTRUCTIVE)
      document.body.appendChild(parts.fixture.nativeElement)
      parts.trigger()?.click()
      await parts.fixture.whenStable()
      return parts
    }

    it('opens and closes from its button, and says so', async () => {
      const { fixture, trigger, menu } = await render(DESTRUCTIVE)
      expect(menu()?.hidden).toBe(true)
      expect(trigger()?.getAttribute('aria-expanded')).toBe('false')
      trigger()?.click()
      await fixture.whenStable()
      expect(menu()?.hidden).toBe(false)
      expect(trigger()?.getAttribute('aria-expanded')).toBe('true')
      expect(trigger()?.getAttribute('aria-controls')).toBe(menu()?.id)
      trigger()?.click()
      await fixture.whenStable()
      expect(menu()?.hidden).toBe(true)
    })

    it('is a manual popover, shown in the top layer when the browser has them and hidden again', async () => {
      const show = vi.fn()
      const hide = vi.fn()
      Object.assign(HTMLElement.prototype, { showPopover: show, hidePopover: hide })
      try {
        const { fixture, trigger, menu } = await render(DESTRUCTIVE)
        expect(menu()?.getAttribute('popover')).toBe('manual')
        trigger()?.click()
        await fixture.whenStable()
        expect(show).toHaveBeenCalledTimes(1)
        trigger()?.click()
        await fixture.whenStable()
        expect(hide).toHaveBeenCalledTimes(1)
        // Destroyed while open: it is let go of too.
        trigger()?.click()
        await fixture.whenStable()
        fixture.destroy()
        expect(hide).toHaveBeenCalledTimes(2)
      } finally {
        delete (HTMLElement.prototype as Partial<HTMLElement>).showPopover
        delete (HTMLElement.prototype as Partial<HTMLElement>).hidePopover
      }
    })

    it('listens to the document and the window only while it is open', async () => {
      const add = vi.spyOn(document, 'addEventListener')
      const remove = vi.spyOn(document, 'removeEventListener')
      const addWindow = vi.spyOn(window, 'addEventListener')
      const { fixture, trigger } = await render(DESTRUCTIVE)
      const names = (spy: { mock: { calls: unknown[][] } }) => spy.mock.calls.map((call) => call[0])
      expect(names(add)).not.toContain('click')
      expect(names(addWindow)).not.toContain('resize')
      trigger()?.click()
      await fixture.whenStable()
      expect(names(add)).toEqual(expect.arrayContaining(['click', 'scroll']))
      expect(names(addWindow)).toContain('resize')
      trigger()?.click()
      await fixture.whenStable()
      expect(names(remove)).toEqual(expect.arrayContaining(['click', 'scroll']))
    })

    it('moves focus into the menu with one tab stop, and moves it with the arrow keys, wrapping, Home and End', async () => {
      const { menu, items, fixture } = await opened()
      expect(document.activeElement).toBe(items()[0])
      expect(items().map((item) => item.getAttribute('tabindex'))).toEqual(['0', '-1', '-1'])
      const press = (key: string) => menu()?.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true }))
      press('ArrowDown')
      expect(document.activeElement).toBe(items()[1])
      await fixture.whenStable()
      expect(items().map((item) => item.getAttribute('tabindex'))).toEqual(['-1', '0', '-1'])
      press('ArrowDown')
      press('ArrowDown')
      expect(document.activeElement).toBe(items()[0])
      press('ArrowUp')
      expect(document.activeElement).toBe(items()[2])
      press('ArrowUp')
      expect(document.activeElement).toBe(items()[1])
      press('Home')
      expect(document.activeElement).toBe(items()[0])
      press('End')
      expect(document.activeElement).toBe(items()[2])
      press('x')
      expect(document.activeElement).toBe(items()[2])
      // With focus on none of the items, Up goes to the last and Down to the first.
      ;(document.activeElement as HTMLElement).blur()
      press('ArrowUp')
      expect(document.activeElement).toBe(items()[2])
      ;(document.activeElement as HTMLElement).blur()
      press('ArrowDown')
      expect(document.activeElement).toBe(items()[0])
    })

    it('closes on Tab and lets focus go on, and returns focus to its button on Escape; Escape while closed does nothing', async () => {
      const { fixture, trigger, menu } = await opened()
      const tab = new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true })
      menu()?.dispatchEvent(tab)
      await fixture.whenStable()
      expect(menu()?.hidden).toBe(true)
      expect(tab.defaultPrevented).toBe(false)
      fixture.nativeElement.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
      expect(menu()?.hidden).toBe(true)
      trigger()?.click()
      await fixture.whenStable()
      fixture.nativeElement.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
      await fixture.whenStable()
      expect(menu()?.hidden).toBe(true)
      expect(document.activeElement).toBe(trigger())
    })

    it('closes on a click outside and a resize, and not on a click inside', async () => {
      const { fixture, trigger, menu, root } = await render(DESTRUCTIVE)
      const open = async () => {
        trigger()?.click()
        await fixture.whenStable()
        expect(menu()?.hidden).toBe(false)
      }
      await open()
      root.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      expect(menu()?.hidden).toBe(false)
      document.body.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      await fixture.whenStable()
      expect(menu()?.hidden).toBe(true)
      await open()
      window.dispatchEvent(new Event('resize'))
      await fixture.whenStable()
      expect(menu()?.hidden).toBe(true)
      // Closed: a click outside does nothing and nothing is listening.
      document.body.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      expect(menu()?.hidden).toBe(true)
    })

    it('follows its button when the page scrolls, instead of closing', async () => {
      const { fixture, trigger, menu } = await render(DESTRUCTIVE)
      place(trigger(), { right: 600, bottom: 100, top: 72 })
      trigger()?.click()
      await fixture.whenStable()
      expect(menu()?.style.top).toBe('104px')
      place(trigger(), { right: 600, bottom: 60, top: 32 })
      document.dispatchEvent(new Event('scroll'))
      await fixture.whenStable()
      expect(menu()?.hidden).toBe(false)
      expect(menu()?.style.top).toBe('64px')
    })

    it('emits, closes and returns focus to its button when an item is chosen', async () => {
      const { fixture, trigger, items, activations, menu } = await opened()
      items()[1].click()
      await fixture.whenStable()
      expect(activations.map((activation) => activation.action.id)).toEqual(['d2'])
      expect(menu()?.hidden).toBe(true)
      expect(document.activeElement).toBe(trigger())
    })

    it('shows the reason of a disabled item and does nothing when it is chosen', async () => {
      const parts = await render([registryAction('d1', 'Discard', { safety: 'destructive', applicable: false, reason: 'Unpushed commits' })])
      parts.trigger()?.click()
      await parts.fixture.whenStable()
      expect(text(parts.items()[0])).toBe('Discard Unpushed commits')
      parts.items()[0].click()
      await parts.fixture.whenStable()
      expect(parts.activations).toEqual([])
      expect(parts.menu()?.hidden).toBe(false)
    })

    it('sits below its button, aligned to its right edge and kept on the screen, or above it near the foot', async () => {
      const { fixture, trigger, menu } = await render(DESTRUCTIVE)
      const open = async (rect: Partial<DOMRect>, width = 1024, height = 768) => {
        place(trigger(), rect, width, height)
        trigger()?.click()
        await fixture.whenStable()
        const style = menu()?.style
        const position = [style?.left, style?.top, style?.bottom]
        trigger()?.click()
        await fixture.whenStable()
        return position
      }
      expect(await open({ right: 600, bottom: 100, top: 72 })).toEqual(['376px', '104px', ''])
      expect(await open({ right: 100, bottom: 100, top: 72 })).toEqual(['8px', '104px', ''])
      expect(await open({ right: 1300, bottom: 100, top: 72 })).toEqual(['792px', '104px', ''])
      expect(await open({ right: 600, bottom: 700, top: 672 })).toEqual(['376px', '', '100px'])
      expect(await open({ right: 300, bottom: 100, top: 72 }, 200)).toEqual(['8px', '104px', ''])
    })

    it('stops listening for scrolls when destroyed', async () => {
      const { fixture, trigger } = await render(DESTRUCTIVE)
      const remove = vi.spyOn(document, 'removeEventListener')
      trigger()?.click()
      await fixture.whenStable()
      fixture.destroy()
      expect(remove).toHaveBeenCalledWith('scroll', expect.any(Function), true)
    })
  })
})
