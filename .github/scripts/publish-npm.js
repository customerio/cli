#!/usr/bin/env node

const { execFileSync } = require("child_process");
const fs = require("fs");
const path = require("path");

const ROOT = path.resolve(__dirname, "..", "..");
const NPM_DIR = path.join(ROOT, "npm");
const VERSION = process.env.VERSION || "";
const dryRun = process.argv.includes("--dry-run");
const checkOnly = process.argv.includes("--check");
const unknownArgs = process.argv
  .slice(2)
  .filter((arg) => arg !== "--dry-run" && arg !== "--check");

if (unknownArgs.length > 0) {
  console.error(`Unknown argument(s): ${unknownArgs.join(", ")}`);
  process.exit(1);
}
if (dryRun && checkOnly) {
  console.error("--dry-run and --check cannot be used together");
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

const packageDirs = platforms.map((platform) => {
  const packageDir = path.join(NPM_DIR, `cli-${platform.npm}`);
  const packagePath = path.join(packageDir, "package.json");
  if (!fs.existsSync(packagePath)) {
    fail(`missing generated package metadata: ${packagePath}`);
  }
  const pkg = JSON.parse(fs.readFileSync(packagePath, "utf8"));
  assertPackageMetadata(pkg, `@customerio/cli-${platform.npm}`);
  return packageDir;
});

assertPackageMetadata(rootPackage, EXPECTED_ROOT_PACKAGE);

for (const packageDir of packageDirs) {
  publish(packageDir);
}
publish(ROOT);
