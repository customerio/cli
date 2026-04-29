#!/usr/bin/env node

const { spawnSync } = require("child_process");
const { existsSync } = require("fs");
const path = require("path");

const pkgRoot = path.join(__dirname, "..");
const rootPkg = require(path.join(pkgRoot, "package.json"));
const optionalDependencies = rootPkg.optionalDependencies || {};
const platform = (rootPkg.customerioCli?.platforms || []).find(
  (candidate) => candidate.os === process.platform && candidate.cpu === process.arch
);

if (!platform) {
  console.error(
    `Unsupported platform for ${rootPkg.name}: ${process.platform}-${process.arch}`
  );
  process.exit(1);
}

const platformPackage = Object.keys(optionalDependencies).find((packageName) =>
  packageName.endsWith(`-${platform.npm}`)
);
let platformPackageRoot;

if (!platformPackage) {
  console.error(
    `Missing optional dependency metadata for ${process.platform}-${process.arch}.`
  );
  process.exit(1);
}

try {
  platformPackageRoot = path.dirname(
    require.resolve(`${platformPackage}/package.json`, { paths: [pkgRoot] })
  );
} catch {
  console.error(
    `Missing optional dependency ${platformPackage} for ${process.platform}-${process.arch}.\n` +
      "Reinstall without disabling optional dependencies."
  );
  process.exit(1);
}

const binPath = path.join(platformPackageRoot, "bin", `cio${platform.ext || ""}`);

if (!existsSync(binPath)) {
  console.error(
    `Missing ${rootPkg.name} binary at ${binPath}.\n` +
      "Reinstall without disabling optional dependencies."
  );
  process.exit(1);
}

const result = spawnSync(binPath, process.argv.slice(2), {
  stdio: "inherit",
});

if (result.error) {
  console.error(result.error.message);
  process.exit(1);
}

if (result.signal) {
  try {
    process.kill(process.pid, result.signal);
  } catch {
    // Some platforms cannot re-raise child signals; use a generic failure below.
  }
  process.exit(1);
}

process.exit(result.status ?? 1);
