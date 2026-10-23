import { FleetModels } from '@cockpit/fleet-data'
import { buildRepositories } from '@cockpit/fleet-data/list'
import { fleetDocument, repository } from '@cockpit/fleet-data/testing'
import { findByAddress } from './repository-address'

const rows = buildRepositories(
  new FleetModels().forDocument(
    fleetDocument({
      repositories: [
        repository('a1', 'alpha', { name: 'Acme/Tool', host: 'github.com' }),
        repository('a2', 'alpha', { name: 'acme/tool', host: 'gitlab.example.com' }),
        repository('a3', 'alpha', { name: 'acme/solo', host: undefined }),
      ],
    }),
    Date.parse('2026-10-01T10:05:00Z'),
  ),
)

describe('findByAddress', () => {
  it('finds the repository whose owner and name are those, whatever their case', () => {
    expect(findByAddress(rows, '-', 'ACME', 'solo')?.id).toBe('a3')
    expect(findByAddress(rows, 'github.com', 'acme', 'solo')?.id).toBe('a3')
  })

  it('takes the host to tell two repositories of one name apart, and `-` for the one with no host', () => {
    expect(findByAddress(rows, 'GitLab.example.com', 'acme', 'tool')?.id).toBe('a2')
    expect(findByAddress(rows, 'github.com', 'acme', 'tool')?.id).toBe('a1')
    expect(findByAddress(rows, '-', 'acme', 'tool')?.id).toBe('a1')
  })

  it('finds nothing for a repository that is not there', () => {
    expect(findByAddress(rows, 'github.com', 'acme', 'nope')).toBeUndefined()
  })
})
