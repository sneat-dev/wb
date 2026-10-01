import { Routes } from '@angular/router'

/** The preview build's replacement of gallery-routes.ts: one lazy route that shows every control-surface component in every state. */
export const galleryRoutes: Routes = [{ path: 'gallery', title: 'Gallery', loadComponent: () => import('./gallery-page').then((m) => m.GalleryPage) }]
