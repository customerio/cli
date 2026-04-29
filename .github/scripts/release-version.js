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

module.exports = {
  normalizeVersion,
};
