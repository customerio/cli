function normalizeVersion(input) {
  const match = String(input || "")
    .trim()
    .match(/^v?(\d+\.\d+\.\d+(?:-([a-zA-Z][a-zA-Z0-9]*(?:\.[a-zA-Z0-9]+)*))?)$/);

  if (!match) {
    throw new Error(
      "version must use X.Y.Z or X.Y.Z-<prerelease> format (e.g. 1.2.3, 1.2.3-alpha.1)"
    );
  }

  const version = match[1];
  const prerelease = match[2] || null;
  const tag = `v${version}`;
  const distTag = prerelease ? prerelease.split(".")[0] : "latest";

  return {
    npmVersion: version,
    tag,
    tagRef: `refs/tags/${tag}`,
    prerelease,
    distTag,
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
