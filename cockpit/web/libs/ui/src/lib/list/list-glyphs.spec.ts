import { GLYPH_CODE, GLYPH_EXTERNAL_LINK } from '../control/glyphs'
import { GLYPH_OPEN } from './list-glyphs'

describe('the glyphs of a row', () => {
  it('draws "open the page", the code browser and the external host differently, so three links on a row are told apart', () => {
    const drawn = [GLYPH_OPEN, GLYPH_CODE, GLYPH_EXTERNAL_LINK].map((paths) => paths.join('|'))
    expect(new Set(drawn).size).toBe(3)
  })
})
