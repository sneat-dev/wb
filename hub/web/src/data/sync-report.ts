export interface SyncReportLocation {
  repository: string;
  commitSHA: string;
  reportID: string;
  githubDirectoryURL: string;
  recordPrefix: string;
}

const repositoryPattern = /^[A-Za-z0-9][A-Za-z0-9._-]*\/[A-Za-z0-9][A-Za-z0-9._-]*$/;
const commitPattern = /^[0-9a-f]{40}$/;
const reportPattern = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/;

export function syncReportLocation(search: URLSearchParams): SyncReportLocation | undefined {
  const repository = search.get('repo') ?? '';
  const commitSHA = search.get('ref') ?? '';
  const reportID = search.get('report') ?? '';
  if (!repositoryPattern.test(repository) || !commitPattern.test(commitSHA) || !reportPattern.test(reportID)) return undefined;

  const [owner, name] = repository.split('/');
  const path = [owner, name, 'tree', commitSHA, 'sync-reports', '$records'].map(encodeURIComponent).join('/');
  return {
    repository,
    commitSHA,
    reportID,
    githubDirectoryURL: `https://github.com/${path}`,
    recordPrefix: `${reportID}--`,
  };
}
