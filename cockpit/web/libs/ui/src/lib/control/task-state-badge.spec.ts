import { TestBed } from '@angular/core/testing'
import { TaskStateBadge } from './task-state-badge'

async function render(value: string | undefined, size?: 'small') {
  const fixture = TestBed.createComponent(TaskStateBadge)
  fixture.componentRef.setInput('value', value)
  if (size) fixture.componentRef.setInput('size', size)
  await fixture.whenStable()
  return (fixture.nativeElement as HTMLElement).querySelector('.badge') as HTMLElement
}

describe('TaskStateBadge', () => {
  it('shows a colour role, a glyph and the word of a task state, with the kind said ahead for assistive technology', async () => {
    const badge = await render('at-risk', 'small')
    expect(badge.classList.contains('tone-bad')).toBe(true)
    expect(badge.classList.contains('small')).toBe(true)
    expect(badge.querySelector('svg path')).not.toBeNull()
    expect(badge.querySelector('.text')?.textContent).toBe('at risk')
    expect(badge.querySelector('.visually-hidden')?.textContent).toBe('Task state: ')
  })

  it('shows an absent value as a grey dashed "state not reported" and a stranger as not recognised', async () => {
    const absent = await render(undefined)
    expect(absent.classList.contains('unreported')).toBe(true)
    expect(absent.querySelector('.text')?.textContent).toBe('state not reported')
    const stranger = await render('exploding')
    expect(stranger.querySelector('.text')?.textContent).toBe('exploding')
    expect(stranger.querySelector('.text')?.getAttribute('aria-hidden')).toBe('true')
    expect(stranger.querySelector('.visually-hidden')?.textContent).toBe('Task state: not recognised')
  })
})
