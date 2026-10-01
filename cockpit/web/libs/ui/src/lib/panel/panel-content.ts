import { ChangeDetectionStrategy, Component, computed, input, signal } from '@angular/core'
import { RouterLink } from '@angular/router'
import { AppLink, PanelCommand } from '@cockpit/fleet-data'
import { AgeText } from '../list/age-text'
import { CopyButton } from '../list/copy-button'
import { Glyph } from '../list/glyph'

/** One fact of a panel's summary. */
export interface PanelFact {
  label: string
  text?: string
  /** A time in epoch milliseconds, shown as a relative age with the exact time on hover (instead of `text`). */
  time?: number
  /** The exact value, where `text` is shortened. */
  title?: string
  /** An address inside the application. */
  link?: AppLink
  /** Offers a copy button for `text`. */
  copy?: boolean
  muted?: boolean
}

/** One related entity: a link inside the application, an external address, or plain text. */
export interface PanelRelatedItem {
  text: string
  link?: AppLink
  /** An external address, opened in a new tab; the caller passes only addresses it has checked. */
  href?: string
  title?: string
}

export interface PanelRelated {
  title: string
  items: readonly PanelRelatedItem[]
}

/**
 * The content of one entity, which is the side panel's content and the detail
 * page's, rendered by this one component (REQ:detail-routes-share-the-panel):
 * the heading, the summary facts, the related entities, what the page projects
 * (the default slot), the action area (`[panelActions]`, which task 13 fills
 * and which vanishes while empty), the "Copy command" entries, and the
 * collapsed "Raw data" block, which renders the entries exactly as the read
 * model sent them only once it is opened. Leave `commands` empty where a
 * projected `[panelCommands]` list takes their place.
 */
@Component({
  selector: 'app-panel-content',
  imports: [RouterLink, AgeText, CopyButton, Glyph],
  templateUrl: './panel-content.html',
  styleUrl: './panel-content.css',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class PanelContent {
  /** What kind of entity it is, shown above the heading: "Worktree". */
  readonly kind = input.required<string>()
  readonly heading = input.required<string>()
  /** Offers a copy button for the heading (a task or branch name, a session id). */
  readonly copyHeading = input(false)
  readonly facts = input<readonly PanelFact[]>([])
  readonly related = input<readonly PanelRelated[]>([])
  readonly commands = input<readonly PanelCommand[]>([])
  readonly raw = input<readonly unknown[]>([])
  /** The detail page, not the side panel: it has no border and its own width. */
  readonly page = input(false)

  protected readonly rawOpen = signal(false)
  protected readonly json = computed(() => JSON.stringify(this.raw(), null, 2))
}
