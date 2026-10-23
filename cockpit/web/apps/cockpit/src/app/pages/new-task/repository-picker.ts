import { ChangeDetectionStrategy, Component, computed, input, output, signal } from '@angular/core'
import { pickRepositories } from '@cockpit/fleet-data/commands'

/** How many matches the list shows; the rest are reached by narrowing the filter. */
export const PICKER_ROWS = 8

let nextId = 0

/**
 * The repository picker of the New task form (REQ:new-task-form): one input that uses the library's matcher (the grammar
 * of the list filters, wildcards included) over the repositories, offering only names that are `owner/name` of letters,
 * digits, dots, underscores and hyphens. The matches are a keyboard list (a combobox: arrow keys move, Enter chooses),
 * a pattern's matches can be chosen all at once, and what is chosen shows as removable chips. It holds no data: the
 * form owns what is chosen.
 */
@Component({
  selector: 'app-repository-picker',
  templateUrl: './repository-picker.html',
  styleUrl: './repository-picker.css',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class RepositoryPicker {
  /** Every repository `owner/name` the fleet has. */
  readonly names = input.required<readonly string[]>()
  readonly chosen = input.required<readonly string[]>()
  /** The clock of the matcher's `age:` terms, in epoch milliseconds. */
  readonly now = input.required<number>()

  /** The names chosen to add, in the order of the list. */
  readonly added = output<string[]>()
  readonly removed = output<string>()

  protected readonly id = `repository-picker-${nextId++}`
  protected readonly text = signal('')
  protected readonly focused = signal(false)
  protected readonly active = signal(0)

  /** The offered names matching the text, not yet chosen. */
  protected readonly matches = computed(() => {
    const chosen = new Set(this.chosen())
    return pickRepositories(this.names(), this.text(), this.now()).filter((name) => !chosen.has(name))
  })
  protected readonly shown = computed(() => this.matches().slice(0, PICKER_ROWS))
  protected readonly open = computed(() => (this.focused() || this.text() !== '') && this.matches().length > 0)
  protected readonly activeId = computed(() => (this.open() ? `${this.id}-option-${this.active()}` : null))

  protected edit(value: string): void {
    this.text.set(value)
    this.active.set(0)
  }

  protected press(event: KeyboardEvent): void {
    const count = this.shown().length
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      event.preventDefault()
      if (count > 0) this.active.set((this.active() + (event.key === 'ArrowDown' ? 1 : count - 1)) % count)
    } else if (event.key === 'Enter') {
      event.preventDefault()
      if (event.ctrlKey || event.metaKey) this.addAll()
      else this.choose(this.shown()[this.active()])
    } else if (event.key === 'Escape' && this.text() !== '') {
      event.stopPropagation()
      this.edit('')
    }
  }

  protected choose(name: string | undefined): void {
    if (name === undefined) return
    this.added.emit([name])
    this.active.set(Math.min(this.active(), Math.max(0, this.shown().length - 2)))
  }

  protected addAll(): void {
    if (this.matches().length === 0) return
    this.added.emit(this.matches())
    this.edit('')
  }
}
