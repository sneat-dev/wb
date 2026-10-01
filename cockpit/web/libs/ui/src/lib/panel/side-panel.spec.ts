import { MediaMatcher } from '@angular/cdk/layout'
import { Component } from '@angular/core'
import { TestBed } from '@angular/core/testing'
import { SHEET_QUERY } from './sheet-mode'
import { SidePanel } from './side-panel'

@Component({
  imports: [SidePanel],
  template: `<app-side-panel label="Details"><button id="inner-a">a</button><button id="inner-b">b</button></app-side-panel>`,
})
class Host {}

/** A phone-sized screen or not; the listeners the panel registered are kept so a test can resize it. */
const listeners = new Set<(event: { matches: boolean }) => void>()
function phone(matches: boolean) {
  listeners.clear()
  const matchMedia = (media: string) => ({ matches, media, addListener: (listener: never) => listeners.add(listener), removeListener: (listener: never) => listeners.delete(listener) })
  TestBed.configureTestingModule({ providers: [{ provide: MediaMatcher, useValue: { matchMedia } }] })
}

function key(target: Element, init: KeyboardEventInit): KeyboardEvent {
  const event = new KeyboardEvent('keydown', { bubbles: true, cancelable: true, ...init })
  target.dispatchEvent(event)
  return event
}

describe('SidePanel', () => {
  // cockpit-views#ac:side-panel-opens-and-closes
  it('is a labelled region beside the list with a close button that asks to close', async () => {
    phone(false)
    const fixture = TestBed.createComponent(SidePanel)
    fixture.componentRef.setInput('label', 'Worktree fix-ci')
    const closed = vi.fn()
    fixture.componentInstance.closed.subscribe(closed)
    await fixture.whenStable()
    const aside = fixture.nativeElement.querySelector('aside') as HTMLElement
    expect(aside.getAttribute('role')).toBe('complementary')
    expect(aside.getAttribute('aria-label')).toBe('Worktree fix-ci')
    expect(aside.hasAttribute('aria-modal')).toBe(false)
    expect(SHEET_QUERY).toContain('47.99rem')
    ;(fixture.nativeElement.querySelector('button.close') as HTMLButtonElement).click()
    expect(closed).toHaveBeenCalledTimes(1)
  })

  it('takes the focus when it opens as a phone sheet, not beside the list, and moves it on request', async () => {
    phone(false)
    const beside = TestBed.createComponent(SidePanel)
    beside.componentRef.setInput('label', 'x')
    await beside.whenStable()
    expect(document.activeElement).not.toBe(beside.nativeElement.querySelector('aside'))
    beside.componentInstance.focus()
    expect(document.activeElement).toBe(beside.nativeElement.querySelector('aside'))
    TestBed.resetTestingModule()
    phone(true)
    const sheet = TestBed.createComponent(SidePanel)
    sheet.componentRef.setInput('label', 'x')
    await sheet.whenStable()
    expect(document.activeElement).toBe(sheet.nativeElement.querySelector('aside'))
  })

  it('stops the page behind a sheet from scrolling, and restores it when closed', async () => {
    phone(true)
    document.body.style.overflow = 'auto'
    const fixture = TestBed.createComponent(SidePanel)
    fixture.componentRef.setInput('label', 'x')
    await fixture.whenStable()
    expect(document.body.style.overflow).toBe('hidden')
    fixture.destroy()
    expect(document.body.style.overflow).toBe('auto')
    document.body.style.overflow = ''
    TestBed.resetTestingModule()
    phone(false)
    const beside = TestBed.createComponent(SidePanel)
    beside.componentRef.setInput('label', 'x')
    await beside.whenStable()
    expect(document.body.style.overflow).toBe('')
  })

  it('becomes the sheet when the screen narrows, becomes a region again when it widens, and stops listening when destroyed', async () => {
    phone(false)
    const fixture = TestBed.createComponent(SidePanel)
    fixture.componentRef.setInput('label', 'x')
    await fixture.whenStable()
    const aside = fixture.nativeElement.querySelector('aside') as HTMLElement
    expect(listeners.size).toBe(1)
    listeners.forEach((listener) => listener({ matches: true }))
    await fixture.whenStable()
    expect(aside.getAttribute('role')).toBe('dialog')
    listeners.forEach((listener) => listener({ matches: false }))
    await fixture.whenStable()
    expect(aside.getAttribute('role')).toBe('complementary')
    TestBed.resetTestingModule()
    expect(listeners.size).toBe(0)
  })

  it('does not trap Tab beside the list', async () => {
    phone(false)
    const fixture = TestBed.createComponent(Host)
    await fixture.whenStable()
    const last = fixture.nativeElement.querySelector('#inner-b') as HTMLElement
    document.body.append(fixture.nativeElement)
    last.focus()
    expect(key(last, { key: 'Tab' }).defaultPrevented).toBe(false)
    fixture.nativeElement.remove()
  })

  it('is a full-screen modal dialog on a phone, and keeps Tab inside it', async () => {
    phone(true)
    const standalone = TestBed.createComponent(SidePanel)
    standalone.componentRef.setInput('label', 'Details')
    await standalone.whenStable()
    const sheet = standalone.nativeElement.querySelector('aside') as HTMLElement
    expect(sheet.getAttribute('role')).toBe('dialog')
    expect(sheet.getAttribute('aria-modal')).toBe('true')

    TestBed.resetTestingModule()
    phone(true)
    const fixture = TestBed.createComponent(Host)
    await fixture.whenStable()
    document.body.append(fixture.nativeElement)
    const aside = fixture.nativeElement.querySelector('aside') as HTMLElement
    const close = aside.querySelector('button.close') as HTMLElement
    const lastOne = aside.querySelector('#inner-b') as HTMLElement
    lastOne.focus()
    expect(key(lastOne, { key: 'Tab' }).defaultPrevented).toBe(true)
    expect(document.activeElement).toBe(close)
    expect(key(close, { key: 'Tab', shiftKey: true }).defaultPrevented).toBe(true)
    expect(document.activeElement).toBe(lastOne)
    aside.focus()
    expect(key(aside, { key: 'Tab', shiftKey: true }).defaultPrevented).toBe(true)
    // In the middle, and for other keys, the browser's own order stands.
    ;(aside.querySelector('#inner-a') as HTMLElement).focus()
    expect(key(aside, { key: 'Tab' }).defaultPrevented).toBe(false)
    expect(key(aside, { key: 'Tab', shiftKey: true }).defaultPrevented).toBe(false)
    expect(key(aside, { key: 'x' }).defaultPrevented).toBe(false)
    fixture.nativeElement.remove()
  })
})
