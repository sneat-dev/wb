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
| `app-state-badge` | `kind`, `value`, `label?`, `size` (`normal` 24 px, `small` 20 px), `hint?` | One badge for every vocabulary: `task`, `owner`, `agent-activity`, `agent-state`, `pr-state`, `mergeable`, `code-index`, `route`, `load`, `checks`. Colour role + glyph + word; the kind is read ahead of the word by assistive technology. An absent or unknown value is a grey dashed "not reported" (the raw value is shown, never guessed). The tables are one module per kind in `state-tables/` (`badgeSpec(kind, value)` carries all of them; `ownerBadgeSpec`, `taskBadgeSpec` and the other per-kind functions of `state-vocabulary.ts` carry only theirs, for a page that shows one kind). |
| `app-sync-badges` | `ahead`, `behind`, `upstreamGone`, `hasUpstream` | `↑n`, `↓n`, "gone", "no upstream". Renders nothing when no fact applies (cached worktrees), so no gap. |
| `app-pr-chip` | `pullRequest` | `#n` (link only for a checked `webAddress` of the library: https, a plain host), state, `passed/total` checks (verdict is the daemon's `checks_green`), the failed check name truncated with the full name as `title`, observation age or "not yet checked". |
| `app-machine-chip` | `machine`, `stale` | Name, route, cached age, stale mark, transport (`http`/`ssh`). |
| `app-relative-time` | `at` | `<time datetime>` (ISO UTC) with the time in the viewer's zone and the UTC value as tooltip; "age unknown" for an absent time. It reads the shared `UiClock` (one timer, replaceable in a test), so rows pass no clock down. |
| `app-glyph` | `paths` | Inline SVG (CSP: no data: images, no icon font). `glyphs.ts` has one `GLYPH_*` export per glyph, so a page bundles only those it names. |

## Copy command

`app-copy-command-list [entries]` renders `PanelCommand[]` from `@cockpit/fleet-data` (the entity panels'
`commands`). Per entry: title, the command in monospace on one line, a copy button (Clipboard API with a
selected-textarea fallback that restores focus; "Copied" feedback, polite announcement), "run here" or the library's label ("run on vm"),
an "edit before running" mark when `needsEdit`: the button reads "Copy template", the announcement is "Copied; edit the <…> parts before running", and only the library's own `PLACEHOLDERS` values are highlighted, and for a refused command the
library's reason and no button. It prints only text the library produced and builds none; an empty list renders
nothing. `ClipboardWriter` and `app-copy-button` are reusable.

## Action slot

`app-action-slot [actions] [target] (activated)`. `actions` is what the registry returned for `<type>:<id>`
(`RegistryAction[]`), `undefined` when the route is absent. only `safe` and `guarded` are buttons; every other class (`destructive`, unknown or missing) sits under an overflow
menu, so the slot fails closed (cockpit-actions REQ:common-actions-are-direct). An action that cannot run is
`aria-disabled` (still focusable), its reason read once as its description and shown in words on hover, focus and tap; a not-permitted action
carries the one constant explanation `OWNER_ONLY_EXPLANATION` instead, never its own sign-in text. With no registry or
no action the host (`display: contents`) renders nothing: no placeholder, no gap. Activating emits
`{action, target}` synchronously: no navigation, no request; the preview and execution are cockpit-actions.

The menu is a manual popover (top layer, so a transformed or clipped row cannot cut it) with a fixed-box fallback; its
document, resize and scroll listeners exist only while it is open; Tab closes it, Escape and choosing return focus to its
button, and the items are one roving tab stop.

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
hidden data table (the text alternative; a series of more than 60 points is its min, average, max and latest plus every
tenth point); clickable buckets are buttons in it named "label: value", shown while focused. A value that is not a
finite number is a gap or no bar; an empty chart says "No data"; a Chart.js chunk that cannot be fetched says "Chart
unavailable" and the table stays; a change of `spec.kind` destroys and recreates the chart.

## Gallery

`/gallery` exists only in the preview build: `pnpm build:preview` (configuration `preview`, output `dist-preview/`)
swaps `gallery/gallery-routes.ts` for `gallery-routes.preview.ts`. `pnpm shots` photographs it too when
`dist-preview/` exists. `pnpm test:e2e` builds the preview first and the stubbed e2e (`control.e2e.ts`) serves both builds.
`finish-build` fails the production build if `app-gallery` or the fixture marker is in any `dist/*.js`, and an e2e checks that
production `/gallery` is not a route.
