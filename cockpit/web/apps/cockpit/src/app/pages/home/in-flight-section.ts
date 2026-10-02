import { ChangeDetectionStrategy, Component, computed, input } from '@angular/core'
import { RouterLink } from '@angular/router'
import { AppLink, CopyCommand, FleetModel, InFlightAgent, agentDetailLink, chipLink, formatAge, linkTarget } from '@cockpit/fleet-data'
import { GLYPH_CHECK_CIRCLE, Glyph, LazyCopy, RelativeTime, StateBadge } from '@cockpit/ui/control'
import { SkeletonRows } from '../../shell/skeleton-rows'
import { isoOf } from './home-format'
import { spanText } from './home-time'
import { MachineWords, machineWords } from './machine-words'
import { MachineStrip } from './machine-strip'

/** One running agent as a row shows it. */
export interface FlightRow {
  id: string
  /** Runtime and model. */
  label: string
  link: AppLink
  /** The task, or "session, started 2 h ago" when the agent has no work link. */
  work: string
  machine: MachineWords | undefined
  /** On another machine: the snapshot's age is shown and there is no action. */
  remote: boolean
  observedAt: string | undefined
  /** How long it has run; absent when its start is not reported. */
  runningFor: string | undefined
  activity: InFlightAgent['activity']
  /** Stop and Log are offered only for a dispatched run on this machine. */
  controllable: boolean
  stop: () => Promise<CopyCommand>
  log: () => Promise<CopyCommand>
}

/** `wb agent <verb> '<agent-id>'`: the lazy entry point of the library, loaded when a button is pressed. */
const agentCommand = (verb: 'agentStop' | 'agentLogs', id: string) => async (): Promise<CopyCommand> => (await import('@cockpit/fleet-data/commands'))[verb](id)

/** The words of the machine an agent runs on; undefined when the document does not list it. */
function machineOf(model: FleetModel, machineId: string): MachineWords | undefined {
  const view = model.machines.find((candidate) => candidate.machine.id === machineId)
  return view && machineWords(view, model.now)
}

/** The rows of "In flight": the running agents on every machine, this machine's first. */
export function flightRows(model: FleetModel): FlightRow[] {
  return model.inFlight.map((flight) => ({
    id: flight.agent.id,
    label: flight.label,
    link: agentDetailLink(flight.agent.id),
    work: flight.task ?? (flight.agent.started_at === undefined ? 'session' : `session, started ${formatAge(flight.agent.started_at, model.now)}`),
    machine: machineOf(model, flight.machineId),
    remote: flight.remote,
    observedAt: flight.observedAt,
    runningFor: flight.startedAt === undefined ? undefined : spanText(model.now - flight.startedAt),
    activity: flight.activity,
    controllable: flight.controllable,
    stop: agentCommand('agentStop', flight.agent.id),
    log: agentCommand('agentLogs', flight.agent.id),
  }))
}

/**
 * Home "In flight" (REQ:home-in-flight): the running agents on every machine that reports them,
 * with the machine strip at the right. An agent from another machine shows the age of its snapshot
 * and no action; Stop and Log are "Copy command" entries and only for dispatched runs here (an
 * agent has no registry target type).
 */
@Component({
  selector: 'app-in-flight',
  imports: [RouterLink, Glyph, LazyCopy, MachineStrip, RelativeTime, SkeletonRows, StateBadge],
  templateUrl: './in-flight-section.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class InFlightSection {
  protected readonly target = linkTarget
  readonly model = input.required<FleetModel>()
  /** The daemon's first scan is still running: an empty section is not yet an answer. */
  readonly warming = input(false)

  protected readonly rows = computed(() => flightRows(this.model()))
  /** Some machine's agents were cut, so the count is a least. */
  protected readonly cut = computed(() => this.model().agentsCut)
  /** The other machines that report no agents, in one muted line. */
  protected readonly silent = computed(() =>
    this.model()
      .machines.filter((view) => !view.local && view.runningAgents === 0)
      .map((view) => view.machine.machine),
  )
  protected readonly idle = GLYPH_CHECK_CIRCLE
  protected readonly runningLink = chipLink('agents', 'running')
  protected readonly iso = isoOf
}
