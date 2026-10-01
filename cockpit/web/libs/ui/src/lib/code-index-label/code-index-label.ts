import { ChangeDetectionStrategy, Component, input } from '@angular/core'
import { CodeIndex, codeIndexText, formatAge } from '@cockpit/fleet-data'

/**
 * The code-index freshness of a checkout: one label per configured indexer,
 * the state as text (a stale index adds how many commits it is behind), then
 * how long ago the receipt it was read from was written, as visible text, with
 * the exact time as the tooltip. An entry whose freshness is not known shows a
 * dash.
 */
@Component({
  selector: 'app-code-index-label',
  templateUrl: './code-index-label.html',
  styleUrl: './code-index-label.css',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class CodeIndexLabel {
  readonly states = input<CodeIndex[] | undefined>()
  /** The clock the age is measured against, in epoch milliseconds. */
  readonly now = input.required<number>()

  protected readonly text = codeIndexText
  protected readonly age = formatAge
}
