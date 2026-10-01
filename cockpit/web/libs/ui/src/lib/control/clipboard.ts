import { DOCUMENT } from '@angular/common'
import { Injectable, inject } from '@angular/core'

/**
 * Puts text on the clipboard: the asynchronous Clipboard API where the page may
 * use it (a secure context such as the loopback address), else a selected
 * off-screen text field and `execCommand('copy')`. Both are plain function
 * calls from a click handler: no inline handler and no inline style attribute,
 * so the strict content security policy is unchanged.
 */
@Injectable({ providedIn: 'root' })
export class ClipboardWriter {
  private readonly document = inject(DOCUMENT)

  /** Whether the text was copied. */
  async copy(text: string): Promise<boolean> {
    const clipboard = this.document.defaultView?.navigator?.clipboard
    if (clipboard?.writeText) {
      try {
        await clipboard.writeText(text)
        return true
      } catch {
        // Refused (no permission or no focus): the fallback may still work.
      }
    }
    return this.fallback(text)
  }

  private fallback(text: string): boolean {
    const field = this.document.createElement('textarea')
    field.value = text
    field.setAttribute('readonly', '')
    field.setAttribute('aria-hidden', 'true')
    // The style properties go through the CSS object model, which the style nonce does not restrict.
    field.style.position = 'fixed'
    field.style.top = '0'
    field.style.opacity = '0'
    this.document.body.appendChild(field)
    field.select()
    try {
      return this.document.execCommand('copy')
    } catch {
      return false
    } finally {
      field.remove()
    }
  }
}
