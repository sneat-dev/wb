import { TestBed } from '@angular/core/testing'
import { registryAction } from '@cockpit/fleet-data/testing'
import { ClipboardWriter } from '@cockpit/ui/control'
import { HomeRegistry, PUSH_ACTION } from './home-registry'
import { WorkOffer } from './needs-you-rows'
import { WorkAction } from './work-action'

const work: WorkOffer = {
  task: 'fix-ci',
  worktrees: [
    { id: 'w1', branch: 'task/fix-ci', repository: 'acme/a' },
    { id: 'w2', branch: 'task/fix-ci-b', repository: 'acme/b' },
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
    // The one slot has no handler, so it is the Copy control and nothing else.
    expect(root.querySelectorAll('app-action-slot')).toHaveLength(1)
    expect(root.querySelectorAll('app-action-slot .slot')).toHaveLength(0)
    const buttons = root.querySelectorAll('button')
    expect(buttons).toHaveLength(1)
    expect(buttons[0].textContent?.trim()).toBe('Copy template')
    expect(buttons[0].getAttribute('aria-label')).toBe('Copy template wb pr create: commit everything and open the pull request of acme/a and acme/b')
    buttons[0].click()
    await vi.waitFor(() => expect(copy).toHaveBeenCalledWith("wb pr create 'fix-ci' --commit-all --message=<<<edit:message>>>"))
    fixture.detectChanges()
  })

  // cockpit-views#ac:action-area-renders-the-registry-and-vanishes-without-it
  it('is still only the Copy control when the registry offers a push: no page handles it, so it is not a button', async () => {
    const { root } = await render({ 'worktree:w2': true })
    expect(root.querySelectorAll('.slot, .action')).toHaveLength(0)
    expect([...root.querySelectorAll('button')].map((button) => button.textContent?.trim())).toEqual(['Copy template'])
    // The slot is for the worktree the registry offered the push for.
    expect(root.querySelector('app-action-slot')).not.toBeNull()
  })

  // cockpit-views#ac:home-needs-you-work-at-risk
  it('names both repositories in the button of a task at risk in two, and neither for a task in one', async () => {
    const { root } = await render({})
    expect(root.querySelector('button')?.getAttribute('aria-label')).toContain('of acme/a and acme/b')
    TestBed.resetTestingModule()
    TestBed.configureTestingModule({ providers: [{ provide: HomeRegistry, useValue: { offered: () => undefined } }, { provide: ClipboardWriter, useValue: { copy: vi.fn() } }] })
    const fixture = TestBed.createComponent(WorkAction)
    fixture.componentRef.setInput('action', { task: 'one', worktrees: [{ id: 'w1', branch: 'one', repository: 'acme/a' }, { id: 'w3', branch: 'one-b', repository: 'acme/a' }] })
    await fixture.whenStable()
    expect(fixture.nativeElement.querySelector('button').getAttribute('aria-label')).toBe('Copy template wb pr create: commit everything and open the pull request')
  })

  it('offers one Copy for the task however many worktrees the registry has a push for', async () => {
    const { root } = await render({ 'worktree:w1': true, 'worktree:w2': true })
    expect(root.querySelectorAll('button')).toHaveLength(1)
    expect(root.querySelectorAll('.home-slot')).toHaveLength(0)
  })
})
