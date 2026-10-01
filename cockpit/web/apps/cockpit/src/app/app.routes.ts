import { Type } from '@angular/core'
import { Route, Routes } from '@angular/router'

// Every route of the application, registered once and lazy-loaded: the tab
// list (nav.ts) and this table are complete, and the task that builds a page
// replaces the body of its file under pages/<name>/ without editing either.
// A page keeps the exported class name its route imports.
//
// A page that still uses PrimeNG components is routed through `prime(...)`, which
// loads pages/prime-theme.ts (PrimeNG and the Cockpit theme) only for that route:
// the shell and every page without PrimeNG never fetch it. New pages use none.
//
// Titles are the page names: the document title and the visually hidden `h1`
// of the shell. `/dashboard` is the old name of Home and shows it. A detail
// route names its path parameter `id` where it has one: input binding would
// otherwise hand it to the `repository` input of a list page.

const prime = (path: string, title: string, load: () => Promise<Type<unknown>>): Route => ({
  path,
  title,
  loadChildren: () => import('./pages/prime-theme').then((m) => m.primePage(load)),
})

const page = (path: string, title: string, loadComponent: Route['loadComponent']): Route => ({ path, title, loadComponent })

export const pageRoutes: Routes = [
  { ...page('', 'Home', () => import('./pages/home/home-page').then((m) => m.HomePage)), pathMatch: 'full' },
  { path: 'dashboard', redirectTo: '' },
  page('tasks', 'Tasks', () => import('./pages/tasks/tasks-page').then((m) => m.TasksPage)),
  page('tasks/new', 'New task', () => import('./pages/new-task/new-task-page').then((m) => m.NewTaskPage)),
  page('tasks/detail', 'Task', () => import('./pages/tasks/task-detail-page').then((m) => m.TaskDetailPage)),
  prime('repositories', 'Repositories', () => import('./pages/repositories/repositories-page').then((m) => m.RepositoriesPage)),
  page('repositories/:host/:owner/:name', 'Repository', () => import('./pages/repositories/repository-detail-page').then((m) => m.RepositoryDetailPage)),
  page('repositories/:id', 'Repository', () => import('./pages/repositories/repository-page').then((m) => m.RepositoryPage)),
  page('worktrees', 'Worktrees', () => import('./pages/worktrees/worktrees-page').then((m) => m.WorktreesPage)),
  page('worktrees/:id', 'Worktree', () => import('./pages/worktrees/worktree-page').then((m) => m.WorktreePage)),
  prime('agents', 'Agents', () => import('./pages/agents/agents-page').then((m) => m.AgentsPage)),
  page('agents/:id', 'Agent', () => import('./pages/agents/agent-detail-page').then((m) => m.AgentDetailPage)),
  prime('machines', 'Machines', () => import('./pages/machines/machines-page').then((m) => m.MachinesPage)),
  page('machines/:id', 'Machine', () => import('./pages/machines/machine-detail-page').then((m) => m.MachineDetailPage)),
  { path: '**', redirectTo: '' },
]

export const appRoutes: Routes = pageRoutes
