import { Routes } from '@angular/router'

// Every page loads on first use. The filters a list page honours arrive as
// query parameters and are bound to its inputs. A detail route names its path
// parameter `id`: input binding would otherwise hand it to the `repository`
// input. The repository page is the only one that loads the Markdown parser.
export const appRoutes: Routes = [
  { path: '', pathMatch: 'full', redirectTo: 'dashboard' },
  { path: 'dashboard', title: 'Dashboard · WB Cockpit', loadComponent: () => import('./pages/dashboard-page').then((m) => m.DashboardPage) },
  { path: 'repositories', title: 'Repositories · WB Cockpit', loadComponent: () => import('./pages/repositories-page').then((m) => m.RepositoriesPage) },
  { path: 'repositories/:id', title: 'Repository · WB Cockpit', loadComponent: () => import('./pages/repository-page').then((m) => m.RepositoryPage) },
  { path: 'worktrees', title: 'Worktrees · WB Cockpit', loadComponent: () => import('./pages/worktrees-page').then((m) => m.WorktreesPage) },
  { path: 'worktrees/:id', title: 'Worktree · WB Cockpit', loadComponent: () => import('./pages/worktree-page').then((m) => m.WorktreePage) },
  { path: 'agents', title: 'Agents · WB Cockpit', loadComponent: () => import('./pages/agents-page').then((m) => m.AgentsPage) },
  { path: 'machines', title: 'Machines · WB Cockpit', loadComponent: () => import('./pages/machines-page').then((m) => m.MachinesPage) },
  { path: '**', redirectTo: 'dashboard' },
]
