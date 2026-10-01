# Control surface (libs/ui)

The components that make state, commands and actions look the same everywhere. They are standalone, take plain
inputs, hold no data of their own, and execute nothing: an action is an emitted event, a command is text the
library built. Import them from their own entry points, never from the barrel (`@cockpit/ui`), which also exports
the PrimeNG-based list components:

- `@cockpit/ui/control`: everything below except the charts.
- `@cockpit/ui/chart`: the chart component and presets (Chart.js is its own lazy chunk behind it).

Nothing here uses PrimeNG. Colour comes from `apps/cockpit/src/styles/tokens.css` only (state roles `--ok`,
`--warn`, `--bad`, `--idle` with `-soft` and `-border`, the one accent, `--chart-*`), so light and dark follow
the browser. Contract: `spec/features/cockpit-views/README.md` (REQ:action-slots, REQ:copy-the-command,
REQ:owner-gating-is-visible, REQ:intent-to-done-budgets, REQ:look-*, REQ:strict-csp-unchanged).

## State and chips

| Component | Inputs | Notes |
|---|---|---|
| `app-state-badge` | `kind`, `value`, `label?`, `size` (`normal` 24 px, `small` 20 px), `hint?` | One badge for every vocabulary: `task`, `owner`, `agent-activity`, `agent-state`, `pr-state`, `mergeable`, `code-index`, `route`, `load`, `checks`. Colour role + glyph + word; the kind is read ahead of the word by assistive technology. An absent or unknown value is a grey dashed "not reported" (the raw value is shown, never guessed). The table is `state-vocabulary.ts` (`badgeSpec`). |
| `app-sync-badges` | `ahead`, `behind`, `upstreamGone`, `hasUpstream` | `↑n`, `↓n`, "gone", "no upstream". Renders nothing when no fact applies (cached worktrees), so no gap. |
| `app-pr-chip` | `pullRequest`, `now` | `#n` (link only for http(s)), state, `passed/total` checks (verdict is the daemon's `checks_green`), the failed check name truncated with the full name as `title`, observation age or "not yet checked". |
| `app-machine-chip` | `machine`, `stale`, `now` | Name, route, cached age, stale mark, transport (`http`/`ssh`). |
| `app-relative-time` | `at`, `now` | `<time>` with the exact time as tooltip; "age unknown" for an absent time. |
| `app-glyph` | `name` | The inline SVG glyph set (CSP: no data: images, no icon font). |

## Copy command

`app-copy-command-list [entries]` renders `PanelCommand[]` from `@cockpit/fleet-data` (the entity panels'
`commands`). Per entry: title, the command in monospace on one line, a copy button (Clipboard API with a
selected-textarea fallback, "Copied" feedback, polite announcement), "run here" or the library's label ("run on vm"),
an "edit before running" mark with the `<placeholders>` highlighted when `needsEdit`, and for a refused command the
library's reason and no button. It prints only text the library produced and builds none; an empty list renders
nothing. `ClipboardWriter` and `app-copy-button` are reusable.

## Action slot

`app-action-slot [actions] [target] (activated)`. `actions` is what the registry returned for `<type>:<id>`
(`RegistryAction[]`), `undefined` when the route is absent. `safe` and `guarded` actions are buttons, `destructive`
ones sit under an overflow menu (cockpit-actions REQ:common-actions-are-direct). An action that cannot run is
`aria-disabled` (still focusable) with the registry's reason as `title` and description; a not-permitted action
carries the one constant explanation `OWNER_ONLY_EXPLANATION` instead, never its own sign-in text. With no registry or
no action the host (`display: contents`) renders nothing: no placeholder, no gap. Activating emits
`{action, target}` synchronously: no navigation, no request; the preview and execution are cockpit-actions.

## Owner gating

`app-owner-signin` is the one place that says "Sign in as owner: run `wb cockpit`", with the command to copy. The shell
shows it in a card (`shell/owner-popover.ts`, in the lazy overlays chunk) opened from the anonymous session chip.

## Charts

`app-chart [spec] [height] (bucketSelected)`; specs from `@cockpit/ui/chart`: `TimeSeriesSpec` (use
`machineMetricSpecs(samples, now)` for CPU, load, memory and disk over the last hour, with a gap wherever samples are
absent), `BarsSpec` (bars by day), `HorizontalBarsSpec` (buckets; a bar with a `link` is clickable and emits it).
Chart.js 4.5.1 is pinned exactly; `chart-engine.ts` is the only module that imports it and registers only bar and
line controllers and elements, the category and linear scales, the filler and the tooltip. It is reached by a dynamic
import, so it is a separate chunk fetched only by a page that shows a chart. The canvas is themed from the `--chart-*`
tokens (resolved through a probe element, because a canvas cannot read `light-dark()`), redrawn when the colour scheme
or reduced-motion preference changes, and not animated for `prefers-reduced-motion`. Every chart carries a visually
hidden data table (the text alternative); clickable buckets are buttons in it, shown while focused.

## Gallery

`/gallery` exists only in the preview build: `pnpm build:preview` (configuration `preview`, output `dist-preview/`)
swaps `gallery/gallery-routes.ts` for `gallery-routes.preview.ts`. `pnpm shots` photographs it too when
`dist-preview/` exists. The stubbed e2e (`control.e2e.ts`) serves both builds.
