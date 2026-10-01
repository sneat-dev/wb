import { Routes } from '@angular/router'

// Every page loads on first use. The filters a list page honours arrive as
// query parameters and are bound to its inputs. A future detail route must name
// its path parameter `id`: input binding would otherwise hand it to the
// `repository` input.
export const appRoutes: Routes = [
  { path: '', pathMatch: 'full', redirectTo: 'dashboard' },
  { path: 'dashboard', title: 'Dashboard · WB Cockpit', loadComponent: () => import('./pages/dashboard-page').then((m) => m.DashboardPage) },
  { path: 'repositories', title: 'Repositories · WB Cockpit', loadComponent: () => import('./pages/repositories-page').then((m) => m.RepositoriesPage) },
  { path: 'worktrees', title: 'Worktrees · WB Cockpit', loadComponent: () => import('./pages/worktrees-page').then((m) => m.WorktreesPage) },
  { path: 'agents', title: 'Agents · WB Cockpit', loadComponent: () => import('./pages/agents-page').then((m) => m.AgentsPage) },
  { path: 'machines', title: 'Machines · WB Cockpit', loadComponent: () => import('./pages/machines-page').then((m) => m.MachinesPage) },
  { path: '**', redirectTo: 'dashboard' },
]
