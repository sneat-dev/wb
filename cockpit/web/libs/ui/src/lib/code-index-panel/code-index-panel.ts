import { ChangeDetectionStrategy, Component, computed, input } from '@angular/core'
import { CodeIndex, Entry } from '@cockpit/fleet-data'
import { codeIndexView } from '@cockpit/fleet-data/list'
import { CodeIndexLabel } from '../code-index-label/code-index-label'

/**
 * The code-index panel of one checkout: whether an index exists, its freshness
 * and its statistics (files, symbols and edges, with symbols by kind) as the
 * configured provider reports them. Says so when the entry is another machine's
 * (its snapshot carries no code index), when no provider is configured and when
 * the checkout is not indexed.
 *
 * The totals are statistics of an index, not counts of Cockpit entities, so
 * they are plain text and carry no hover card or drill-down link.
 */
@Component({
  selector: 'app-code-index-panel',
  imports: [CodeIndexLabel],
  templateUrl: './code-index-panel.html',
  styleUrl: './code-index-panel.css',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class CodeIndexPanel {
  readonly entry = input.required<Entry>()
  readonly states = input<CodeIndex[] | undefined>()
  /** The configured provider's name from the fleet document; none when no provider is configured. */
  readonly provider = input<string | undefined>()
  /** The clock the receipt age is measured against, in epoch milliseconds. */
  readonly now = input.required<number>()

  protected readonly view = computed(() => codeIndexView(this.entry(), this.states(), this.provider()))
}
