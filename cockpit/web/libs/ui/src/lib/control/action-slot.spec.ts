import { TestBed } from '@angular/core/testing'
import { ActionsResponse, RegistryAction } from '@cockpit/fleet-data'
import { fakeControlFetch, registryAction } from '@cockpit/fleet-data/testing'
import { ActionActivation, ActionSlot, OWNER_ONLY_EXPLANATION, disabledReason } from './action-slot'

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

async function render(actions: readonly RegistryAction[] | undefined, target = 'worktree:wt-1') {
  TestBed.resetTestingModule()
  const fixture = TestBed.createComponent(ActionSlot)
  fixture.componentRef.setInput('actions', actions)
  fixture.componentRef.setInput('target', target)
  const activations: ActionActivation[] = []
  fixture.componentInstance.activated.subscribe((activation) => activations.push(activation))
  await fixture.whenStable()
  const root: HTMLElement = fixture.nativeElement
  const buttons = () => [...root.querySelectorAll<HTMLButtonElement>('.slot > button.action:not(.more)')]
  const trigger = () => root.querySelector<HTMLButtonElement>('.more')
  const menu = () => root.querySelector<HTMLElement>('.menu')
  const items = () => [...root.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')]
  return { fixture, root, activations, buttons, trigger, menu, items }
}

const text = (element: Element | null | undefined) => element?.textContent?.replace(/\s+/g, ' ').trim()

describe('ActionSlot', () => {
  afterEach(() => vi.restoreAllMocks())

  // cockpit-views#ac:action-area-renders-the-registry-and-vanishes-without-it
  it('renders exactly what the registry returns: direct buttons, the destructive action under the overflow menu, the disabled one with its reason', async () => {
    const actions = await actionsFor('worktree:wt-1', REGISTRY)
    const { buttons, trigger, items, root } = await render(actions)
    expect(buttons().map((button) => text(button))).toEqual(['Open pull request', 'An action nobody has heard of'])
    expect(buttons().every((button) => !button.classList.contains('disabled'))).toBe(true)
    expect(trigger()?.getAttribute('aria-haspopup')).toBe('menu')
    expect(items().map((item) => text(item.firstElementChild))).toEqual(['Discard worktree'])
    expect(items()[0].getAttribute('aria-disabled')).toBe('true')
    expect(items()[0].getAttribute('title')).toBe('2 commits are not on the remote')
    expect(items()[0].getAttribute('aria-describedby')).toBe(root.querySelector('.menu .visually-hidden')?.id)
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
      // The registry answering again fills it in place.
      fixture.componentRef.setInput('actions', REGISTRY['pull_request:pr-1'])
      await fixture.whenStable()
      expect(root.querySelector('.slot')).not.toBeNull()
    }
  })

  it('has no overflow control when no action is destructive', async () => {
    const { trigger, menu } = await render(REGISTRY['pull_request:pr-1'])
    expect(trigger()).toBeNull()
    expect(menu()).toBeNull()
  })

  it('disables a not-applicable action with the registry reason, a default one when it gives none, and keeps it focusable', async () => {
    const { buttons } = await render([
      registryAction('a', 'First', { applicable: false, reason: 'Nothing to commit' }),
      registryAction('b', 'Second', { applicable: false }),
      registryAction('c', 'Third'),
    ])
    const [first, second, third] = buttons()
    expect(first.getAttribute('aria-disabled')).toBe('true')
    expect(first.hasAttribute('disabled')).toBe(false)
    expect(first.getAttribute('title')).toBe('Nothing to commit')
    expect(first.nextElementSibling?.textContent).toBe('Nothing to commit')
    expect(first.getAttribute('aria-describedby')).toBe(first.nextElementSibling?.id)
    expect(second.getAttribute('title')).toBe('Not available now')
    expect(third.getAttribute('aria-disabled')).toBeNull()
    expect(third.getAttribute('title')).toBeNull()
    expect(third.getAttribute('aria-describedby')).toBeNull()
  })

  // cockpit-views#ac:owner-gating-is-one-affordance
  it('shows every action the caller may not run disabled with the same single explanation, and no sign-in message of its own', async () => {
    const { buttons, items, root } = await render(
      [
        registryAction('a', 'First', { permitted: false }),
        registryAction('b', 'Second', { permitted: false, applicable: false, reason: 'Nothing to commit' }),
        registryAction('c', 'Third', { permitted: false, safety: 'destructive' }),
      ],
    )
    const titles = [...buttons(), ...items()].map((button) => button.getAttribute('title'))
    expect(titles).toEqual([OWNER_ONLY_EXPLANATION, OWNER_ONLY_EXPLANATION, OWNER_ONLY_EXPLANATION])
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
    const DESTRUCTIVE = [registryAction('d1', 'Discard', { safety: 'destructive' }), registryAction('d2', 'Delete branch', { safety: 'destructive' })]

    function place(trigger: HTMLElement | null, rect: Partial<DOMRect>, innerWidth = 1024, innerHeight = 768) {
      vi.spyOn(trigger as HTMLElement, 'getBoundingClientRect').mockReturnValue({ top: 0, bottom: 0, left: 0, right: 0, ...rect } as DOMRect)
      vi.stubGlobal('innerWidth', innerWidth)
      vi.stubGlobal('innerHeight', innerHeight)
    }

    afterEach(() => vi.unstubAllGlobals())

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

    it('moves focus into the menu, moves it with the arrow keys, Home and End, and returns it to the button on Escape', async () => {
      const { fixture, trigger, menu, items } = await render(DESTRUCTIVE)
      document.body.appendChild(fixture.nativeElement)
      trigger()?.click()
      await fixture.whenStable()
      expect(document.activeElement).toBe(items()[0])
      const press = (key: string) => menu()?.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true }))
      press('ArrowDown')
      expect(document.activeElement).toBe(items()[1])
      press('ArrowDown')
      expect(document.activeElement).toBe(items()[0])
      press('ArrowUp')
      expect(document.activeElement).toBe(items()[1])
      press('Home')
      expect(document.activeElement).toBe(items()[0])
      press('End')
      expect(document.activeElement).toBe(items()[1])
      press('x')
      expect(document.activeElement).toBe(items()[1])
      fixture.nativeElement.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
      await fixture.whenStable()
      expect(menu()?.hidden).toBe(true)
      expect(document.activeElement).toBe(trigger())
      // Escape with the menu closed does nothing.
      fixture.nativeElement.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
      expect(menu()?.hidden).toBe(true)
      fixture.nativeElement.remove()
    })

    it('closes on a click outside, a resize and a scroll, and not on a click inside', async () => {
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
      await open()
      document.dispatchEvent(new Event('scroll'))
      await fixture.whenStable()
      expect(menu()?.hidden).toBe(true)
      // A click outside while closed does nothing.
      document.body.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      expect(menu()?.hidden).toBe(true)
    })

    it('emits and closes when an item is chosen', async () => {
      const { fixture, trigger, items, activations, menu } = await render(DESTRUCTIVE)
      trigger()?.click()
      await fixture.whenStable()
      items()[1].click()
      await fixture.whenStable()
      expect(activations.map((activation) => activation.action.id)).toEqual(['d2'])
      expect(menu()?.hidden).toBe(true)
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
