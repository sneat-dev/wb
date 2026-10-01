import { ChangeDetectionStrategy, Component, computed, inject, input } from '@angular/core'
import { FleetStore } from '@cockpit/fleet-data'
import { MachineChip } from '../control/machine-chip'

/** A row's machine as the control surface's chip: name, how it is reached, the age of a cached snapshot, stale, transport. */
@Component({
  selector: 'app-machine-cell',
  imports: [MachineChip],
  template: `@if (view(); as machine) {
      <app-machine-chip [machine]="machine.machine" [stale]="machine.state === 'stale'" />
    }`,
  styles: `
    :host {
      display: flex;
      overflow: hidden;
    }
    app-machine-chip {
      flex-wrap: nowrap;
    }
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class MachineCell {
  /** The machine entry id. */
  readonly id = input.required<string>()
  private readonly store = inject(FleetStore)
  protected readonly view = computed(() => this.store.model().machines.find((machine) => machine.machine.id === this.id()))
}
