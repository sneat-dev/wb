# ui

Components the Cockpit pages share. Import each from its own entry point, never from the
`@cockpit/ui` barrel, which also exports the PrimeNG components of the legacy pages:

| Entry point | What it holds |
|---|---|
| `@cockpit/ui/list` | `ListView` (`<app-list>`), `ListCell`, `ListPanelTemplate`, `ListColumn`, `ListChip`, `AgeText`, `RepoName`, `CountLink`, `CopyButton`, `provideListShortcuts` |
| `@cockpit/ui/panel` | `SidePanel`, `PanelContent` with `PanelFact`, `PanelRelated` |
| `@cockpit/ui/code-index-label`, `/code-index-panel`, `/route-label`, `/count` | the single components of those names |

None of `list` and `panel` uses PrimeNG. Tokens come from `apps/cockpit/src/styles/tokens.css`.

## A list page in about 40 lines

`<app-list>` does the filter box (the grammar of REQ:list-filter-and-matcher), the machine and quick-filter
chips, sortable headers, the "37 of 438" count, virtual one-line rows under a sticky header, `j` `k` Enter Esc,
the empty and no-match states, the placeholder rows, the side panel and the whole address state
(`q`, `sort`, `dir`, `machine`, `chips`, `sel`). The page gives rows, columns, chips and templates; filtering,
sorting and the vocabulary come from `@cockpit/fleet-data`.

```ts
@Component({
  selector: 'app-worktrees-page',
  imports: [ListView, ListCell, ListPanelTemplate, AgeText, WorktreePanelView],
  templateUrl: './worktrees-page.html',
  providers: [provideListShortcuts(Shortcuts)], // `/` and Esc reach this list (shell/shortcuts)
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class WorktreesPage {
  protected readonly store = inject(FleetStore)
  protected readonly rows = computed(() => this.store.model().worktreeRows) // ListRow<Worktree>[]
  protected readonly chips: ListChip[] = [{ id: 'active', label: 'Active' }, { id: 'unpushed', label: 'Unpushed', hint: 'this machine only' }]
  protected readonly columns: ListColumn<Worktree>[] = [
    { id: 'worktree', header: 'Worktree', sort: 'worktree', width: 'fill', grow: 3, min: 132, text: (w) => w.task },
    { id: 'branch', header: 'Branch', width: 'fill', empty: (w) => w.branch === w.task, drop: 'phone' }, // hidden while uniform
    { id: 'activity', header: 'Last activity', sort: 'activity', width: 104, keep: 6 },
  ]
}
```

```html
<app-list page="worktrees" noun="worktrees" [rows]="rows()" [columns]="columns" [chips]="chips">
  <ng-template appCell="worktree" let-w>{{ w.task }}</ng-template>
  <ng-template appCell="branch" let-w>{{ w.branch }}</ng-template>
  <ng-template appCell="activity" let-w><app-age [at]="w.last_activity_at" /></ng-template>
  <ng-template appListPanel let-w><app-worktree-panel [id]="w.id" /></ng-template>
</app-list>
```

* `page` is a `ListPageId`; its chips, sort ids, `sel` key and default sort are the vocabulary's. Every chip
  the page lists must be one of `VOCABULARY[page].chips`.
* A column's `empty` says a row's value is empty or the default; a column that is so for every visible row is
  hidden. At most 7 columns show: past that the lowest `keep` goes first. `drop` removes a column on a
  narrow list (`narrow`, a panel beside it) or a phone (`phone`). `text` is the cell's `title` while it
  truncates; a cell with its own markup sets its own.
* Cells: `app-age` (relative time, exact time in `title`, muted over 30 days), `app-repo-name` (muted
  `owner/`, strong name, host only when the fleet has two), `app-count-link` (a count as a link from the
  fleet-data link helpers, or plain text with the reason as its `title`), `app-copy-button` (full value to
  the clipboard; `[tabbable]="false"` inside a row).
* `resolve` finds a row from `sel` when the id is not the row's (the merged Repositories rows);
  `panelLabel` names the panel for assistive technology.

## The panel and the detail page

`<app-panel-content>` is the content of one entity: heading, summary facts, related entities, projected
content, the action area and the "Copy command" entries, and the collapsed "Raw data" block (rendered, as
the read model sent it, only once opened). The side panel (`<app-side-panel>`, which `<app-list>` hosts) and
the detail route are two hosts of one component: `worktree-panel.ts` is the pattern, and `worktree-page.ts`
renders it with `[page]="true"`.

* Task 13 puts its action slot into the content's `[panelActions]` projection (the region takes no
  space while empty) and its copy-command list in place of the `commands` input: pass `commands` only while
  the built-in list (title, command, copy button, "edit the placeholder first", "run on mac") is wanted.
* On a phone the panel is a full-screen modal dialog and keeps Tab inside it; beside the list it is a
  complementary region. A selection the operator makes moves the focus into the panel; Esc closes it and
  returns the focus to the list; a pasted `?sel=` opens it without taking the focus.
