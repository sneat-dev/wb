const OWNER_FIELD = "owning repository";
const CHANGE_FIELD = "concrete wb change";
const ATTESTATION_FIELD = "ownership attestation";
const WB_REPOSITORY = "sneat-dev/wb";
const ATTESTATION =
  "I confirm this issue requires a change to WB code, tests, documentation, packaging, or release.";

function withoutComments(value) {
  return value.replace(/<!--[\s\S]*?-->/g, "").trim();
}

function fieldsFrom(body) {
  const fields = new Map();
  const headings = [...body.matchAll(/^#{1,6}\s+(.+?)\s*$/gm)];

  for (let index = 0; index < headings.length; index++) {
    const heading = headings[index];
    const name = heading[1].trim().toLowerCase();
    const valueStart = heading.index + heading[0].length;
    const valueEnd = index + 1 < headings.length ? headings[index + 1].index : body.length;

    if (!fields.has(name)) {
      fields.set(name, body.slice(valueStart, valueEnd));
    }
  }

  return fields;
}

function validateWBIssue(body) {
  const fields = fieldsFrom(typeof body === "string" ? body : "");
  const owner = withoutComments(fields.get(OWNER_FIELD) || "");

  if (!owner) {
    return { valid: false, reason: "missing_owner" };
  }
  if (owner !== WB_REPOSITORY) {
    return { valid: false, reason: "wrong_owner" };
  }
  if (!withoutComments(fields.get(CHANGE_FIELD) || "")) {
    return { valid: false, reason: "missing_concrete_change" };
  }

  const attestation = withoutComments(fields.get(ATTESTATION_FIELD) || "");
  const checkedAttestation = new RegExp(
    `(?:^|\\n)\\s*[-*]\\s*\\[[xX]\\]\\s*${ATTESTATION.replace(/[.*+?^${}()|[\\]\\\\]/g, "\\$&")}\\s*(?:\\n|$)`,
  );
  if (!checkedAttestation.test(attestation)) {
    return { valid: false, reason: "missing_attestation" };
  }

  return { valid: true };
}

module.exports = { validateWBIssue };
