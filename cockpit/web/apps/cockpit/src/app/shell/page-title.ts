import { Injectable, Provider, signal } from '@angular/core'
import { Title } from '@angular/platform-browser'
import { RouterStateSnapshot, TitleStrategy } from '@angular/router'

/** The title of a page that names none. */
export const DEFAULT_TITLE = 'WB Cockpit'

/** The name of the page on screen: the document title and the text of the hidden `h1`. */
@Injectable({ providedIn: 'root' })
export class PageTitle {
  readonly title = signal('')
}

/**
 * Sets the document title to the title of the route (REQ:home-route,
 * REQ:no-visible-page-heading) and hands the same text to the shell's hidden
 * `h1`, so the two never differ.
 */
@Injectable()
export class PageTitleStrategy extends TitleStrategy {
  constructor(
    private readonly document: Title,
    private readonly page: PageTitle,
  ) {
    super()
  }

  override updateTitle(snapshot: RouterStateSnapshot): void {
    const title = this.buildTitle(snapshot) ?? DEFAULT_TITLE
    this.document.setTitle(title)
    this.page.title.set(title)
  }
}

export function providePageTitle(): Provider {
  return { provide: TitleStrategy, useClass: PageTitleStrategy }
}
