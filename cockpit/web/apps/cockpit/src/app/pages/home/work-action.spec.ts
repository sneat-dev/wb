import { TestBed } from '@angular/core/testing'
import { registryAction } from '@cockpit/fleet-data/testing'
import { ClipboardWriter } from '@cockpit/ui/control'
import { HomeRegistry, PUSH_ACTION } from './home-registry'
import { WorkOffer } from './needs-you-rows'
import { WorkAction } from './work-action'

const work: WorkOffer = {
  task: 'fix-ci',
  worktrees: [
    { id: 'w1', branch: 'task/fix-ci' },
    { id: 'w2', branch: 'task/fix-ci-b' },
  ],
}

async function render(offered: Record<string, boolean>, copy = vi.fn().mockResolvedValue(true)) {
  const registry = { offered: (target: string) => (offered[target] ? [registryAction(PUSH_ACTION, 'Push')] : undefined) }
  TestBed.configureTestingModule({
    providers: [
      { provide: HomeRegistry, useValue: registry },
      { provide: ClipboardWriter, useValue: { copy } },
    ],
  })
  const fixture = TestBed.createComponent(WorkAction)
  fixture.componentRef.setInput('action', work)
  await fixture.whenStable()
  const root: HTMLElement = fixture.nativeElement
  return { fixture, root, copy }
}

describe('WorkAction', () => {
  it('offers one "Copy template" for the task when the registry offers no push', async () => {
    const { fixture, root, copy } = await render({})
    expect(root.querySelectorAll('app-action-slot')).toHaveLength(0)
    const buttons = root.querySelectorAll('button')
    expect(buttons).toHaveLength(1)
    expect(buttons[0].textContent?.trim()).toBe('Copy template')
    buttons[0].click()
    await vi.waitFor(() => expect(copy).toHaveBeenCalledWith("wb pr create 'fix-ci' --commit-all --message=<<<edit:message>>>"))
    fixture.detectChanges()
  })

  it("offers the registry's push in a slot for each worktree it offers one for, and the copy for the rest", async () => {
    const { root } = await render({ 'worktree:w1': true })
    const groups = [...root.querySelectorAll('.home-slot')]
    expect(groups.map((group) => group.getAttribute('aria-label'))).toEqual(['task/fix-ci'])
    expect(groups[0].querySelector('app-action-slot button')?.textContent?.trim()).toBe('Push')
    expect(root.querySelectorAll('app-lazy-copy')).toHaveLength(1)
  })

  it('offers only slots, and no copy, when the registry has a push for every worktree', async () => {
    const { root } = await render({ 'worktree:w1': true, 'worktree:w2': true })
    expect(root.querySelectorAll('.home-slot')).toHaveLength(2)
    expect(root.querySelectorAll('app-lazy-copy')).toHaveLength(0)
  })
})
