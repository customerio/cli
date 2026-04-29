#!/usr/bin/env node

const { execFileSync } = require("child_process");
const fs = require("fs");
const os = require("os");
const path = require("path");

const ROOT = path.resolve(__dirname, "..", "..");
const NPM_DIR = path.join(ROOT, "npm");
const VERSION = process.env.VERSION || "";
const dryRun = process.argv.includes("--dry-run");
const checkOnly = process.argv.includes("--check");
const resumeExisting = process.argv.includes("--resume-existing");
const unknownArgs = process.argv
  .slice(2)
  .filter((arg) => !["--dry-run", "--check", "--resume-existing"].includes(arg));

if (unknownArgs.length > 0) {
  console.error(`Unknown argument(s): ${unknownArgs.join(", ")}`);
  process.exit(1);
}
if (dryRun && checkOnly) {
  console.error("--dry-run and --check cannot be used together");
  process.exit(1);
}
if (resumeExisting && (dryRun || checkOnly)) {
  console.error("--resume-existing is only valid for real publish runs");
  process.exit(1);
}

if (!/^v\d+\.\d+\.\d+$/.test(VERSION)) {
  console.error("VERSION must be set to an exact version like v1.2.3");
  process.exit(1);
}

const version = VERSION.slice(1);
const rootPackagePath = path.join(ROOT, "package.json");
const rootPackage = JSON.parse(fs.readFileSync(rootPackagePath, "utf8"));
const platforms = rootPackage.customerioCli?.platforms || [];
const EXPECTED_ROOT_PACKAGE = "@customerio/cli";
const EXPECTED_PLATFORM_PACKAGES = [
  "@customerio/cli-darwin-arm64",
  "@customerio/cli-darwin-x64",
  "@customerio/cli-linux-arm64",
  "@customerio/cli-linux-x64",
  "@customerio/cli-win32-arm64",
  "@customerio/cli-win32-x64",
];

function fail(message) {
  console.error(message);
  process.exit(1);
}

function npmView(packageName) {
  try {
    const output = execFileSync("npm", ["view", `${packageName}@${version}`, "--json"], {
      encoding: "utf8",
      stdio: ["ignore", "pipe", "pipe"],
    }).trim();
    if (!output) {
      return null;
    }
    return JSON.parse(output);
  } catch (err) {
    const output = `${err.stdout || ""}\n${err.stderr || ""}`;
    if (
      /E404|No match found for version|not in this registry|code E404/i.test(output)
    ) {
      return null;
    }
    throw err;
  }
}

function pack(packageDir) {
  const tempDir = fs.mkdtempSync(path.join(os.tmpdir(), "cio-npm-pack-"));
  try {
    const output = execFileSync(
      "npm",
      ["pack", "--json", "--pack-destination", tempDir],
      { cwd: packageDir, encoding: "utf8" }
    );
    const parsed = JSON.parse(output);
    const packed = parsed[0];
    if (!packed?.integrity || !packed?.shasum) {
      fail(`npm pack did not report integrity for ${packageDir}`);
    }
    return packed;
  } finally {
    fs.rmSync(tempDir, { recursive: true, force: true });
  }
}

function assertRemotePackageMatches(packageDir, expectedName, remotePackage) {
  if (remotePackage.name !== expectedName) {
    fail(`remote package name mismatch for ${expectedName}: ${remotePackage.name}`);
  }
  if (remotePackage.version !== version) {
    fail(`remote package version mismatch for ${expectedName}: ${remotePackage.version}`);
  }
  if (remotePackage.repository?.url !== "git+https://github.com/customerio/cli.git") {
    fail(`${expectedName}@${version} remote repository metadata is not expected`);
  }

  const localPack = pack(packageDir);
  if (remotePackage.dist?.integrity !== localPack.integrity) {
    fail(`${expectedName}@${version} already exists with a different tarball integrity`);
  }
  if (remotePackage.dist?.shasum !== localPack.shasum) {
    fail(`${expectedName}@${version} already exists with a different tarball shasum`);
  }
}

function assertPackageMetadata(pkg, expectedName) {
  if (pkg.name !== expectedName) {
    fail(`expected package name ${expectedName}, found ${pkg.name}`);
  }
  if (pkg.version !== version) {
    fail(`expected ${expectedName} version ${version}, found ${pkg.version}`);
  }
  if (pkg.repository?.url !== "git+https://github.com/customerio/cli.git") {
    fail(`${expectedName} must point to git+https://github.com/customerio/cli.git`);
  }
  if (pkg.publishConfig?.access !== "public") {
    fail(`${expectedName} must publish with public access`);
  }
  if (pkg.publishConfig?.provenance !== true) {
    fail(`${expectedName} must publish with provenance enabled`);
  }
}

function assertExactPackageSet(actualNames) {
  const actual = [...actualNames].sort();
  const expected = [EXPECTED_ROOT_PACKAGE, ...EXPECTED_PLATFORM_PACKAGES].sort();
  if (JSON.stringify(actual) !== JSON.stringify(expected)) {
    fail(
      "expected package set:\n" +
        expected.join("\n") +
        "\nactual package set:\n" +
        actual.join("\n")
    );
  }
}

function publish(packageDir) {
  if (checkOnly) {
    return;
  }
  const args = dryRun
    ? ["publish", "--dry-run", "--access", "public"]
    : ["publish", "--access", "public"];
  execFileSync("npm", args, { cwd: packageDir, stdio: "inherit" });
}

if (rootPackage.name !== EXPECTED_ROOT_PACKAGE) {
  fail(`root package must be ${EXPECTED_ROOT_PACKAGE}, found ${rootPackage.name}`);
}
if (!Array.isArray(platforms) || platforms.length === 0) {
  fail("package.json must define customerioCli.platforms");
}

assertExactPackageSet([
  rootPackage.name,
  ...platforms.map((platform) => `${rootPackage.name}-${platform.npm}`),
]);

const packages = platforms.map((platform) => {
  const packageDir = path.join(NPM_DIR, `cli-${platform.npm}`);
  const packagePath = path.join(packageDir, "package.json");
  if (!fs.existsSync(packagePath)) {
    fail(`missing generated package metadata: ${packagePath}`);
  }
  const pkg = JSON.parse(fs.readFileSync(packagePath, "utf8"));
  assertPackageMetadata(pkg, `@customerio/cli-${platform.npm}`);
  return { dir: packageDir, name: pkg.name };
});

assertPackageMetadata(rootPackage, EXPECTED_ROOT_PACKAGE);
packages.push({ dir: ROOT, name: rootPackage.name });

const existingPackages = new Map();
if (!dryRun && !checkOnly) {
  for (const pkg of packages) {
    const remotePackage = npmView(pkg.name);
    if (remotePackage) {
      if (!resumeExisting) {
        fail(`${pkg.name}@${version} already exists; use --resume-existing only after verifying recovery is intended`);
      }
      assertRemotePackageMatches(pkg.dir, pkg.name, remotePackage);
      existingPackages.set(pkg.name, true);
    }
  }
}

for (const pkg of packages) {
  if (existingPackages.has(pkg.name)) {
    console.log(`Skipping existing matching package ${pkg.name}@${version}`);
    continue;
  }
  publish(pkg.dir);
}
