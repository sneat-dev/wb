import { ChangeDetectionStrategy, Component, computed, inject, input } from '@angular/core'
import { FleetModel, MachineLoad, MachineStateId, formatAge, machineLoad } from '@cockpit/fleet-data'
import { StateBadge } from '@cockpit/ui/control'
import { MetricsPoller } from '../../metrics/metrics-poller'
import { sampleWords } from '../machines/machine-text'

export interface MachineTile {
  id: string
  name: string
  state: MachineStateId
  /** The word for how the machine is reached: "local", "live", or "cached 5 min ago". */
  reach: string
  /** free, busy, or not reported: never "free" for a machine with no usable sample. */
  load: MachineLoad
  /** The words of the sample behind the load: its route and age, or that there is none. */
  sample: string
  /** The latest sample's CPU and memory use, whole percent from 0 to 100; absent without a usable sample. */
  bars?: { cpu: number; memory: number }
}

const percent = (value: number): number => Math.min(100, Math.max(0, Math.round(value)))

/** The tile of a machine from the model and its polled metrics. */
export function machineTile(model: FleetModel, view: FleetModel['machines'][number], load: MachineLoad): MachineTile {
  const { machine } = view
  const reach = machine.route === 'local' ? 'local' : machine.route === 'cached' ? `cached ${formatAge(machine.observed_at, model.now)}` : 'live'
  const sample = sampleWords(load, model.now)
  const bars = load.cpuPercent === undefined || load.memoryPercent === undefined ? undefined : { cpu: percent(load.cpuPercent), memory: percent(load.memoryPercent) }
  return { id: machine.id, name: machine.machine, state: view.state, reach, load, sample, bars }
}

/**
 * The machine strip of "In flight" (REQ:home-in-flight): one compact tile per machine with its
 * reach, the load verdict from the polled samples (free below 70 percent CPU and 80 percent
 * memory, busy otherwise, "load unknown" without a sample, never "free" by default) and a small CPU
 * and memory bar when there is a sample. The polling is the page's (`watchMetrics`).
 */
@Component({
  selector: 'app-machine-strip',
  imports: [StateBadge],
  templateUrl: './machine-strip.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class MachineStrip {
  readonly model = input.required<FleetModel>()

  private readonly poller = inject(MetricsPoller)
  protected readonly tiles = computed(() => {
    const entries = this.poller.entries()
    return this.model().machines.map((view) => machineTile(this.model(), view, machineLoad(entries.get(view.machine.id)?.metrics, this.model().now)))
  })
}
