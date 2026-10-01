import { ChangeDetectionStrategy, Component, computed, input, output } from '@angular/core'
import { FormsModule } from '@angular/forms'
import { MachineOption, RepositoryOption } from '@cockpit/fleet-data'
import { SelectModule } from 'primeng/select'

export type FilterKey = 'machine' | 'repository'

/** One filter was set to a value, or cleared (null). */
export interface FilterChange {
  key: FilterKey
  value: string | null
}

/** The machine and repository filters of a list page. */
@Component({
  selector: 'app-filter-bar',
  imports: [FormsModule, SelectModule],
  templateUrl: './filter-bar.html',
  styleUrl: './filter-bar.css',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class FilterBar {
  /** The machines to offer; a machine is selected by its id. */
  readonly machines = input.required<MachineOption[]>()
  /** The repositories to offer; null leaves the repository filter out. */
  readonly repositories = input.required<RepositoryOption[] | null>()
  readonly machine = input<string>()
  readonly repository = input<string>()
  readonly changed = output<FilterChange>()

  /** A filter the URL names that the options do not list stays selectable. */
  protected readonly machineOptions = computed(() => {
    const value = this.machine()
    return !value || this.machines().some((option) => option.id === value)
      ? this.machines()
      : [...this.machines(), { id: value, label: value }]
  })
  protected readonly repositoryOptions = computed(() => {
    const options = this.repositories() ?? []
    const value = this.repository()
    return !value || options.some((option) => option.id === value)
      ? options
      : [...options, { id: value, label: value }]
  })

  protected set(key: FilterKey, value: string | null | undefined): void {
    this.changed.emit({ key, value: value ?? null })
  }
}
