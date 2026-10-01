import { ChangeDetectionStrategy, Component } from '@angular/core'

/**
 * A placeholder for the New task page: its route is registered and lazy-loaded
 * already, so the task that builds the page replaces this file's body and
 * touches neither app.routes.ts nor the tab list.
 */
@Component({
  selector: 'app-new-task-page',
  template: '<p class="placeholder">This page is not built yet.</p>',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class NewTaskPage {}
