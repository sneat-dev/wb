# ui

Components the Cockpit pages share. Import each from its own entry point, never from the
`@cockpit/ui` barrel, which also exports the PrimeNG components of the legacy pages:

| Entry point | What it holds |
|---|---|
| `@cockpit/ui/list` | `ListView` (`<app-list>`), `ListCell`, `ListPanelTemplate`, `ListColumn`, and the cells: `IdentityCell`, `OwnerStateCell`, `PrCell`, `MachineCell`, `CodeIndexCell`, `AgeText`, `RepoName`, `CountLink`, `CopyIcon` |
| `@cockpit/ui/list-host` | `LIST_SHORTCUTS`, which the application provides once (`{ provide: LIST_SHORTCUTS, useExisting: Shortcuts }` in app.config) |
| `@cockpit/ui/panel` | `SidePanel`, `PanelContent` with `PanelFact`, `PanelRelated` |
| `@cockpit/ui/control` | the control surface: state badge, sync badges, PR chip, machine chip, relative time and `UiClock`, copy-command list, action slot |
| `@cockpit/ui/code-index-label`, `/code-index-panel`, `/route-label`, `/count` | the single components of those names |

None of `list`, `panel` and `control` uses PrimeNG. Tokens come from `apps/cockpit/src/styles/tokens.css`.

## A list page in about 25 lines

`<app-list>` does the filter box (the grammar of REQ:list-filter-and-matcher), the machine chips and the page's
quick-filter chips (their labels and hints are the vocabulary's, `VOCABULARY[page].chips`, from `@cockpit/fleet-data/list`), sortable headers, the "37 of 438" count, virtual one-line rows under a sticky header, the keys `j` `k`
PageUp PageDown Home End Enter `o` (open the entity's page) `c` (copy its name) Esc, the empty and no-match
states, the placeholder rows, the side panel and the whole address state (`q`, `sort`, `dir`, `machine`, `chips`,
`sel`; `prefix` renames them for a second list on one page). The page gives its `page` id, the columns, and
templates for the cells that are more than text and for the panel; the noun, the rows, the chips, the address of
an entity's page and the name `c` copies come from the page id; matching, sorting and the link vocabulary
from `@cockpit/fleet-data`. The list is one tab stop: the links and buttons in its rows are out of the tab order.

```ts
@Component({
  selector: 'app-worktrees-page',
  imports: [ListView, ListCell, ListPanelTemplate, IdentityCell, OwnerStateCell, AgeText, WorktreePanelView],
  templateUrl: './worktrees-page.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class WorktreesPage {
  protected readonly columns: ListColumn<Worktree>[] = [
    { id: 'worktree', header: 'Worktree', sort: 'worktree', width: 'fill', grow: 4, min: 300, priority: ALWAYS, value: (w) => w.task },
    { id: 'branch', header: 'Branch', width: 'fill', value: (w) => w.branch, empty: (w) => w.branch === w.task, min: 120, priority: 1 },
    { id: 'state', header: 'State', sort: 'state', width: 250, min: 230, priority: ALWAYS, value: (w) => w.owner_state ?? 'unknown' },
    { id: 'activity', header: 'Last activity', sort: 'activity', width: 104, min: 96, priority: ALWAYS },
  ]
}
```

```html
<app-list page="worktrees" [columns]="columns">
  <ng-template appCell="worktree" let-w><app-identity-cell [name]="w.task" [secondary]="repoName(w)" copyLabel="Copy task name" /></ng-template>
  <ng-template appCell="state" let-w><app-owner-state-cell [worktree]="w" /></ng-template>
  <ng-template appCell="activity" let-w><app-age [at]="w.last_activity_at" /></ng-template>
  <ng-template appListPanel let-w><app-worktree-panel [id]="w.id" /></ng-template>
</app-list>
```

* A column's `value(item)` is its text: the default content of a cell with no template, its `title` while it
  truncates, and what makes it empty. `empty(item)` adds a default that counts as empty (a branch equal to its
  task); a column that is empty for every visible row is hidden, and a fleet of one machine hides Machine.
  At most 7 columns show: past that the lowest `priority` goes first. `width` is the most a column takes and
  `min` the least it needs: when the list is narrower than the `min`s of its columns (a panel beside it, a tablet,
  a phone) the lowest-`priority` column is hidden, the later one first among equals, until the rest fit; `ALWAYS`
  (10) columns are never hidden and share the width instead. The list measures itself, so a panel opening hides
  columns at once; what is hidden is still in the panel. The open-page cell is reserved at the row end.
* Cells: `app-identity-cell` (strong name, muted secondary, copy icon, the name optionally a link),
  `app-owner-state-cell` (owner badge and sync badges), `app-pr-cell` (the control surface's PR chip),
  `app-machine-cell` (its machine chip), `app-code-index-cell`, `app-age` (relative time against the shared
  `UiClock`, exact time in `title`, muted over 30 days), `app-repo-name`, `app-count-link` (a count as a
  link from the fleet-data link helpers, or plain text with the reason as its `title`), `app-copy-icon`
  (full value to the clipboard, shown on the hovered or focused row and always on touch, one live region for
  the whole list). A row passes no clock down.
* `extraChips` adds chips the vocabulary cannot list (the Agents `runtime-<name>`); `resolve` finds a row from
  `sel` when the id is not the row's (the merged Repositories rows); `panelLabel` names the panel.
* A selection that was open and is gone shows "no longer in the fleet" in the panel and offers close.

## The panel and the detail page

`<app-panel-content>` is the content of one entity: heading, summary facts, related entities, projected
content, the action area and the "Copy command" entries, and the collapsed "Raw data" block (rendered, as
the read model sent it, only once opened). The side panel (`<app-side-panel>`, which `<app-list>` hosts) and
the detail route are two hosts of one component: `worktree-panel.ts` is the pattern, and `worktree-page.ts`
renders it with `[page]="true"`.

* The panel renders the control surface's copy-command list from the `commands` input, and its action slot goes
  into the `[panelActions]` projection (a region that takes no space while empty).
* On a phone the panel is a full-screen modal dialog: the focus moves into it, Tab stays inside, the page behind
  does not scroll and the list is inert. Beside the list the focus stays in the list: `j` and `k` keep browsing
  and the panel follows, Enter on the open row (or Tab) moves into the panel, Esc closes it and returns the
  focus to the row that was open. A pasted `?sel=` opens it and scrolls the row into view without taking the focus.
