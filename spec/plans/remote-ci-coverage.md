---
format: https://specscore.md/plan-specification
status: Approved
---
# Plan: Remote CI Coverage Reporting and Collection for `wb`

**Status:** Approved
**Source Feature:** fleet-quality
**Date:** 2026-09-26
**Owner:** alex
**Supersedes:** —

---

## 1. Summary

Provide instantaneous (< 100ms) per-repository and fleet-wide Go test coverage in `sneat-dev/wb` without executing tests locally. Coverage profiles are measured by GitHub Actions CI workflows on merge/push to `main`, published as standardized build artifacts, and harvested by the **Workbench GitHub App** upon receiving the `workflow_run.completed` webhook. Harvested records are stored in the Hub's document store (Firestore on Cloud Run / DALgo on VM/local) and cached locally by `wb` CLI.

---

## 2. Founder Decisions (2026-09-26)

1. **Collection Strategy**: **GitHub App Artifact Harvester**. CI workflows upload a standardized build artifact (`wb-coverage-summary`). The Workbench GitHub App subscribes to `workflow_run.completed` on the default branch and downloads the artifact using its App installation token. Repositories require **zero external secrets or GCP credentials**.
2. **Storage Location**: **Workbench Hub Store (Firestore / DALgo)**. Central coverage records are persisted in a new DALgo collection (`repository_coverage`) hosted on Firestore (Cloud Run) or DALgo (VM/local), with local disk caching (`~/.cache/wb/coverage.json`) and an optional periodic export/sync to `sneat-dev/wb-state`.
3. **CLI Command Interface**: **Dual Interface**.
   - `wb fleet coverage`: Fleet-wide overview table showing latest coverage across all local/enrolled repositories.
   - `wb coverage [repo] --ci`: Flag on existing `wb coverage` to read remote CI coverage for a single repository or the fleet without running tests.
4. **Pilot Engine**: **Local `ingitdb` via DALgo**. For initial test and pilot verification, use `dalgo2ingitdb` (storing inspectable files in `~/.wb/hub`), enabling full local verification without requiring Cloud Run or Firestore infrastructure up front.

---

## 3. End-to-End Architecture

```mermaid
flowchart TD
    subgraph CI["GitHub Actions (Any Fleet Repo)"]
        PUSH[Push to main / Merge PR] --> RUN_TESTS[Run Tests & Measure Coverage]
        RUN_TESTS --> GEN_SUMMARY["wb coverage summary profile.cov --out coverage-summary.json"]
        GEN_SUMMARY --> UPLOAD["actions/upload-artifact@v4\nname: wb-coverage-summary"]
    end

    subgraph GH["GitHub Platform"]
        UPLOAD --> WF_DONE[Workflow Run Completed]
        WF_DONE -->|Webhook: workflow_run.completed| APP_WEBHOOK
        APP_AUTH[GitHub App Token] -.->|GET /actions/runs/{id}/artifacts| ARTIFACT_API[GitHub Artifact API]
    end

    subgraph HUB["Workbench GitHub App & Hub (Cloud Run / VM)"]
        APP_WEBHOOK[Webhook Handler] --> VERIFY{main branch & success?}
        VERIFY -->|Yes| HARVEST[Download & Unpack Zip]
        HARVEST --> VALIDATE[Validate JSON Schema]
        VALIDATE --> STORE[(Hub Store: Firestore / DALgo\nCollection: repository_coverage)]
        STORE -.->|Optional Mirror Sync| STATE_REPO[("sneat-dev/wb-state\n(coverage/{org}/{repo}.json)")]
    end

    subgraph CLIENT["Developer / Agent Host (wb CLI)"]
        CLI_CALL["wb fleet coverage\nor wb coverage --ci"] --> CACHE_CHECK{Local Cache Fresh?\n~/.cache/wb/coverage.json}
        CACHE_CHECK -->|Yes| RENDER[Render Markdown / JSON / Table]
        CACHE_CHECK -->|Miss / Stale| FETCH_HUB["GET /v0/workbench/coverage"]
        FETCH_HUB --> STORE
        FETCH_HUB --> UPDATE_CACHE[Update Local Cache]
        UPDATE_CACHE --> RENDER
    end

    subgraph WEB["Workbench Web Dashboard"]
        DASH[Dashboard UI] --> STORE
    end
```

---

## 4. Technical Specifications

### 4.1 Artifact Contract: `wb-coverage-summary.json`

Uploaded under artifact name `wb-coverage-summary`:

```json
{
  "schema_version": 1,
  "repository": "sneat-dev/wb",
  "commit_sha": "aca7107c8d9e6f3b0e123456789abcdef0123456",
  "ref": "refs/heads/main",
  "workflow_run_id": 3612345678,
  "workflow_run_url": "https://github.com/sneat-dev/wb/actions/runs/3612345678",
  "reported_at": "2026-09-26T11:45:00Z",
  "status": "passed",
  "statements": 91662,
  "covered": 81052,
  "percentage": 88.42,
  "modules": [
    {
      "path": ".",
      "statements": 91662,
      "covered": 81052,
      "percentage": 88.42
    }
  ]
}
```

### 4.2 Hub Document Store Schema: `repository_coverage`

- **Collection**: `repository_coverage`
- **Document Key**: `canonicalRepository(repo)` (e.g. `github.com/sneat-dev/wb`).
- **Backend**: Conforms to `api/githubapp/dalgostore.DocumentStore` interface. Compatible with `dalgo2firestore`, `dalgo2ingitdb`, `dalgo2openvaultdb`, and `dalgo2memory`.

### 4.3 Hub HTTP API

- `GET /v0/workbench/coverage`
  - Headers: `Authorization: Bearer <machine-token>` or Firebase user auth.
  - Query Params: `?filter=<str>` (optional).
  - Response: Array of repository coverage records.
- `GET /v0/workbench/coverage/{owner}/{repo}`
  - Response: Single repository coverage record.

### 4.4 Local CLI Cache

- Location: `~/.cache/wb/coverage.json`
- Default TTL: 15 minutes (overridden by `--refresh` or explicit `--timeout`).

---

## Tasks

### Task 1: Coverage Summary CLI Command & CI Workflow Step

**Id:** task-1
**Verifies:** fleet-quality#ac:instant-remote-ci-coverage
**Depends-On:** —
**Status:** complete

- **Packages**: `cmd/wb`, `internal/quality`
- **Details**:
  - Implement `wb coverage summary <profile.cov> --out <summary.json>` in `cmd/wb/coverage_summary.go`.
  - Extract module statements, covered count, and percentage using existing `quality.ParseCoverageProfile` and `profileTotals`.
  - Update `.github/workflows/go-ci.yml` in `sneat-dev/wb` to generate and upload `wb-coverage-summary` on `push` to `main`.
  - Retain 90 days artifact retention.

### Task 2: Hub Storage Collection & Store Implementation

**Id:** task-2
**Verifies:** fleet-quality#ac:instant-remote-ci-coverage
**Depends-On:** —
**Status:** complete

- **Packages**: `hub`, `api/githubapp`
- **Details**:
  - Register `repositoryCoverageCollection = "repository_coverage"` in `hub/collections.go`.
  - Create `hub/repository_coverage_store.go` and `hub/repository_coverage_store_test.go`.
  - Implement `SaveCoverage(ctx, record)`, `GetCoverage(ctx, repo)`, and `ListCoverage(ctx, repos)`.
  - Validate engine parity in `internal/hubstore` (memory, inGitDB, openvaultdb).

### Task 3: GitHub App `workflow_run` Webhook & Artifact Harvester

**Id:** task-3
**Verifies:** fleet-quality#ac:instant-remote-ci-coverage
**Depends-On:** task-1, task-2
**Status:** complete

- **Packages**: `hub`, `workbench-gh-app`
- **Details**:
  - Extend `hub.translateWebhook` to accept `workflow_run` events.
  - Filter for `action == "completed"`, `conclusion == "success"`, and default branch ref.
  - Implement artifact download in `GitHubAppInstallationVerifier` or dedicated harvester client:
    - Call GitHub API `GET /repos/{owner}/{repo}/actions/runs/{run_id}/artifacts`.
    - Match `name == "wb-coverage-summary"`.
    - Download zip archive, decompress in-memory, parse `coverage-summary.json`.
  - Store record via `RepositoryCoverageStore`.
  - Log event narration: `workflow_run <repo> coverage recorded: XX.XX%`.

### Task 4: Hub API Endpoints & Machine Auth

**Id:** task-4
**Verifies:** fleet-quality#ac:instant-remote-ci-coverage
**Depends-On:** task-2
**Status:** complete

- **Packages**: `hub`, `api/githubapp`
- **Details**:
  - Add routes in `hub/http.go`:
    - `GET /v0/workbench/coverage`
    - `GET /v0/workbench/coverage/{owner}/{repo}`
  - Verify entitlement against caller's enrolled identity/machine.
  - Add test coverage in `hub/coverage_http_test.go`.

### Task 5: `wb` CLI Integration (`wb fleet coverage` & `wb coverage --ci`)

**Id:** task-5
**Verifies:** fleet-quality#ac:instant-remote-ci-coverage
**Depends-On:** task-1, task-2
**Status:** complete

- **Packages**: `cmd/wb`, `internal/quality`
- **Details**:
  - Implement remote coverage client and store adapter.
  - Implement `newFleetCoverageCmd()` under `wb fleet coverage`.
  - Add `--ci` flag to `newCoverageCmd()`.
  - Render terminal-styled Markdown tables, YAML, and JSON.

### Task 6: Fleet Rollout & Dashboard UI

**Id:** task-6
**Depends-On:** task-5
**Status:** queued

- **Packages**: Fleet repos (`dal-go`, `strongo`, `sneat-co`), `workbench-web`
- **Details**:
  - Roll out the `wb-coverage-summary` upload step across fleet CI workflows.
  - Add coverage visual components to Workbench Web dashboard.
  - (Optional) Implement scheduled daemon sync of coverage records into `sneat-dev/wb-state` for offline git provider users.

## Deferred AC Coverage

- fleet-quality#ac:truthful-fleet-coverage — verified by existing local fleet coverage test suite
- fleet-quality#ac:complete-conventional-verification — verified by existing verification test suite
- fleet-quality#ac:exact-graduation-receipt — verified by existing graduation receipt test suite
- fleet-quality#ac:fleet-metrics-web-dashboard — verified by fleet-metrics-web plan

---
*This document follows the https://specscore.md/plan-specification*
