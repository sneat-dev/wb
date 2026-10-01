import { Component, signal } from '@angular/core'
import { TestBed } from '@angular/core/testing'
import { OverlayFocus, restoreFocus, returnTarget } from './overlay-focus'

@Component({
  imports: [OverlayFocus],
  template: `
    <button id="opener">open</button>
    @if (open()) {
      <div appOverlayFocus id="overlay">
        @if (buttons()) {
          <button id="first">one</button>
          <button id="second" [attr.data-autofocus]="marked() ? '' : null">two</button>
          <button id="last">three</button>
        }
      </div>
    }
  `,
})
class Host {
  readonly open = signal(false)
  readonly buttons = signal(true)
  readonly marked = signal(true)
}

function tab(from: Element, shiftKey = false): KeyboardEvent {
  const event = new KeyboardEvent('keydown', { key: 'Tab', shiftKey, bubbles: true, cancelable: true })
  from.dispatchEvent(event)
  return event
}

describe('OverlayFocus', () => {
  async function opened() {
    const fixture = TestBed.createComponent(Host)
    document.body.append(fixture.nativeElement)
    const opener = fixture.nativeElement.querySelector('#opener') as HTMLElement
    opener.focus()
    fixture.componentInstance.open.set(true)
    await fixture.whenStable()
    return { fixture, opener, byId: (id: string) => fixture.nativeElement.querySelector(`#${id}`) as HTMLElement }
  }

  it('focuses the marked element, wraps Tab inside and gives the focus back when it goes', async () => {
    const { fixture, opener, byId } = await opened()
    expect(document.activeElement).toBe(byId('second'))
    expect(tab(byId('second')).defaultPrevented).toBe(false)
    byId('last').focus()
    expect(tab(byId('last')).defaultPrevented).toBe(true)
    expect(document.activeElement).toBe(byId('first'))
    expect(tab(byId('first'), true).defaultPrevented).toBe(true)
    expect(document.activeElement).toBe(byId('last'))
    byId('overlay').focus()
    tab(byId('overlay'))
    expect(document.activeElement).toBe(byId('first'))
    // Other keys pass.
    expect(byId('first').dispatchEvent(new KeyboardEvent('keydown', { key: 'a', cancelable: true, bubbles: true }))).toBe(true)

    fixture.componentInstance.open.set(false)
    await fixture.whenStable()
    expect(document.activeElement).toBe(opener)
    fixture.nativeElement.remove()
  })

  it('takes the focus itself when nothing inside is marked for it', async () => {
    const fixture = TestBed.createComponent(Host)
    document.body.append(fixture.nativeElement)
    fixture.componentInstance.marked.set(false)
    fixture.componentInstance.open.set(true)
    await fixture.whenStable()
    expect(document.activeElement).toBe(fixture.nativeElement.querySelector('#overlay'))
    fixture.nativeElement.remove()
  })

  it('takes the focus itself and holds Tab when nothing inside can', async () => {
    const { fixture, byId } = await opened()
    fixture.componentInstance.buttons.set(false)
    await fixture.whenStable()
    byId('overlay').focus()
    expect(tab(byId('overlay')).defaultPrevented).toBe(true)
    expect(document.activeElement).toBe(byId('overlay'))
    fixture.nativeElement.remove()
  })

  it('restores the focus only to an element that can take it', () => {
    const button = document.createElement('button')
    document.body.append(button)
    restoreFocus(button)
    expect(document.activeElement).toBe(button)
    const gone = document.createElement('button')
    document.body.append(gone)
    gone.remove()
    restoreFocus(gone)
    expect(document.activeElement).toBe(button)
    restoreFocus(null)
    restoreFocus(document.createElementNS('http://www.w3.org/2000/svg', 'svg'))
    expect(document.activeElement).toBe(button)
    button.remove()
  })
})

describe('returnTarget', () => {
  it('is the active element, or what the overlay around it will give back when it is in one', () => {
    const plain = document.createElement('button')
    expect(returnTarget(plain)).toBe(plain)
    expect(returnTarget(null)).toBeNull()
  })

  it('hands the focus back past an overlay that has gone when another opened over it', async () => {
    @Component({
      imports: [OverlayFocus],
      template: `
        <button id="origin">origin</button>
        @if (first()) {
          <div appOverlayFocus id="first"><button id="inside" data-autofocus>in</button></div>
        }
      `,
    })
    class Stack {
      readonly first = signal(false)
    }
    const fixture = TestBed.createComponent(Stack)
    document.body.append(fixture.nativeElement)
    const origin = fixture.nativeElement.querySelector('#origin') as HTMLElement
    origin.focus()
    fixture.componentInstance.first.set(true)
    await fixture.whenStable()
    // A second overlay opens while the focus is inside the first, which then closes.
    expect(returnTarget(document.activeElement)).toBe(origin)
    fixture.nativeElement.remove()
  })
})
