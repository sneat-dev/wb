const assert = require("node:assert/strict");
const test = require("node:test");

const { validateWBIssue } = require("./validate-issue-ownership.cjs");

const validIssue = `### Owning repository

sneat-dev/wb

### Concrete WB change

Add a check to the worktree lifecycle command.

### Ownership attestation

- [x] I confirm this issue requires a change to WB code, tests, documentation, packaging, or release.`;

test("rejects a missing owning repository", () => {
  assert.deepEqual(validateWBIssue(validIssue.replace("sneat-dev/wb", "")), {
    valid: false,
    reason: "missing_owner",
  });
});

test("rejects a wrong owning repository", () => {
  assert.deepEqual(validateWBIssue(validIssue.replace("sneat-dev/wb", "sneat-co/backstage")), {
    valid: false,
    reason: "wrong_owner",
  });
});

test("rejects a missing concrete WB change", () => {
  const body = validIssue.replace(
    "Add a check to the worktree lifecycle command.",
    "<!-- no concrete change -->",
  );
  assert.deepEqual(validateWBIssue(body), {
    valid: false,
    reason: "missing_concrete_change",
  });
});

test("rejects a missing ownership attestation", () => {
  const body = validIssue.replace(
    "- [x] I confirm this issue requires a change to WB code, tests, documentation, packaging, or release.",
    "- [ ] I confirm this issue requires a change to WB code, tests, documentation, packaging, or release.",
  );
  assert.deepEqual(validateWBIssue(body), {
    valid: false,
    reason: "missing_attestation",
  });
});

test("accepts a complete WB-owned issue", () => {
  assert.deepEqual(validateWBIssue(validIssue), { valid: true });
});
