import { MergedRepository } from '@cockpit/fleet-data'

/**
 * The merged repository that `/repositories/:host/:owner/:name` names: the one whose `owner/name` is
 * that, case-insensitively; when two hosts share the name, the one on that host (`-` is no host).
 */
export function findByAddress(rows: readonly MergedRepository[], host: string, owner: string, name: string): MergedRepository | undefined {
  const slug = `${owner}/${name}`.toLowerCase()
  const candidates = rows.filter((row) => row.slug.toLowerCase() === slug)
  const wanted = host === '-' ? undefined : host.toLowerCase()
  return candidates.find((row) => row.host?.toLowerCase() === wanted) ?? candidates[0]
}
