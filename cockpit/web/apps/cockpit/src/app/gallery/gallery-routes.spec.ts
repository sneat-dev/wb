import { galleryRoutes as production } from './gallery-routes'
import { galleryRoutes as preview } from './gallery-routes.preview'

describe('the gallery routes', () => {
  it('are empty in the production build, so the gallery is neither navigable nor in the binary', () => {
    expect(production).toEqual([])
  })

  it('are one lazy route of the preview build, titled Gallery', async () => {
    expect(preview.map((route) => [route.path, route.title])).toEqual([['gallery', 'Gallery']])
    const component = await (preview[0].loadComponent as () => Promise<unknown>)()
    expect(typeof component).toBe('function')
  })
})
