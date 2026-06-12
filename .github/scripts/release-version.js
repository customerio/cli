function normalizeVersion(input) {
  const match = String(input || "")
    .trim()
    .match(/^v?(\d+\.\d+\.\d+)$/);

  if (!match) {
    throw new Error("version must use the exact X.Y.Z or vX.Y.Z format");
  }

  const version = match[1];
  const tag = `v${version}`;
  return {
    npmVersion: version,
    tag,
    tagRef: `refs/tags/${tag}`,
  };
}

function bumpVersion(base, level) {
  const match = String(base || "")
    .trim()
    .match(/^v?(\d+)\.(\d+)\.(\d+)$/);

  if (!match) {
    throw new Error("base version must use the exact X.Y.Z or vX.Y.Z format");
  }

  let [major, minor, patch] = match.slice(1).map(Number);
  switch (level || "patch") {
    case "major":
      major += 1;
      minor = 0;
      patch = 0;
      break;
    case "minor":
      minor += 1;
      patch = 0;
      break;
    case "patch":
      patch += 1;
      break;
    default:
      throw new Error(`bump must be patch, minor, or major, got ${level}`);
  }

  return `${major}.${minor}.${patch}`;
}

module.exports = {
  normalizeVersion,
  bumpVersion,
};
