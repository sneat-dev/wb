import { TestBed } from '@angular/core/testing'
import { SyncBadges } from './sync-badges'

async function render(inputs: Record<string, unknown>) {
  const fixture = TestBed.createComponent(SyncBadges)
  for (const [name, value] of Object.entries(inputs)) fixture.componentRef.setInput(name, value)
  await fixture.whenStable()
  const root: HTMLElement = fixture.nativeElement
  const badges = [...root.querySelectorAll('.sync')].map((badge) => ({ shown: badge.querySelector('[aria-hidden]')?.textContent, name: badge.querySelector('.visually-hidden')?.textContent, title: badge.getAttribute('title') }))
  return { root, badges }
}

describe('SyncBadges', () => {
  it('shows ↑n for unpushed commits and ↓n for commits behind, with the words for assistive technology', async () => {
    const { badges } = await render({ ahead: 2, behind: 1 })
    expect((await render({ behind: 3 })).badges[0].name).toBe('3 commits behind')
    expect(badges).toEqual([
      { shown: '↑2', name: '2 commits not pushed', title: '2 commits not pushed' },
      { shown: '↓1', name: '1 commit behind', title: '1 commit behind' },
    ])
  })

  it('shows "gone" for a gone upstream and "no upstream" for a branch with none', async () => {
    expect((await render({ upstreamGone: true })).badges.map((badge) => badge.shown)).toEqual(['gone'])
    expect((await render({ hasUpstream: false })).badges.map((badge) => badge.shown)).toEqual(['no upstream'])
    expect((await render({ ahead: 1, upstreamGone: true, hasUpstream: false })).badges.map((badge) => badge.shown)).toEqual(['↑1', 'gone', 'no upstream'])
  })

  it('renders nothing for a clean branch, an unknown one or one on another machine, and leaves no box', async () => {
    for (const inputs of [{}, { ahead: 0, behind: 0 }, { upstreamGone: false, hasUpstream: true }]) {
      const { root, badges } = await render(inputs)
      expect(badges).toEqual([])
      expect(root.querySelector('.sync')).toBeNull()
    }
  })
})
