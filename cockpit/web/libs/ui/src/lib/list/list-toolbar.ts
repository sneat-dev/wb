import { ChangeDetectionStrategy, Component, ElementRef, input, output, signal, viewChild } from '@angular/core'
import { MachineOption } from '@cockpit/fleet-data'
import { Glyph } from '../control/glyph'
import { GLYPH_CHECK, GLYPH_HELP, GLYPH_X } from '../control/glyphs'
import { GLYPH_SEARCH } from './list-glyphs'
import { ListChip } from './list-state'

/**
 * The top of a list: the filter box with its grammar hint and clear button, the
 * result count ("37 of 438"), the quick-filter chips and the machine chips. It
 * holds no state of the list: it says what was typed or toggled.
 */
@Component({
  selector: 'app-list-toolbar',
  imports: [Glyph],
  templateUrl: './list-toolbar.html',
  styleUrl: './list-toolbar.css',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ListToolbar {
  readonly id = input.required<string>()
  readonly noun = input.required<string>()
  readonly text = input.required<string>()
  /** The `field:` names the page declares, for the hint. */
  readonly fields = input.required<string>()
  readonly shown = input.required<number>()
  readonly total = input.required<number>()
  /** Some of what is counted may have been left out (an agent list that a machine cut): the count then reads "at least". */
  readonly atLeast = input(false)
  readonly chips = input.required<readonly ListChip[]>()
  readonly activeChips = input.required<readonly string[]>()
  readonly machines = input.required<readonly MachineOption[]>()
  readonly activeMachines = input.required<readonly string[]>()

  readonly edited = output<string>()
  readonly chipToggled = output<string>()
  readonly machineToggled = output<string>()

  private readonly box = viewChild.required<ElementRef<HTMLInputElement>>('box')
  protected readonly hintOpen = signal(false)
  protected readonly search = GLYPH_SEARCH
  protected readonly help = GLYPH_HELP
  protected readonly cross = GLYPH_X
  protected readonly tick = GLYPH_CHECK

  get element(): HTMLInputElement {
    return this.box().nativeElement
  }

  focus(): void {
    this.element.focus()
  }

  isEmpty(): boolean {
    return this.element.value === ''
  }

  /** Empties the box itself as well as the text: a key typed in the frame before has not been rendered into it yet. */
  clear(): void {
    this.element.value = ''
    this.edited.emit('')
  }
}
