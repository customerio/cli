#!/usr/bin/env node

const { execFileSync } = require("child_process");
const fs = require("fs");
const path = require("path");
const { normalizeVersion } = require("./release-version");

const ROOT = path.resolve(__dirname, "..", "..");
const DIST = path.join(ROOT, "dist");
const NPM_DIR = path.join(ROOT, "npm");
const VERSION = process.env.VERSION || "";
const packDryRun = process.argv.includes("--pack-dry-run");
const unknownArgs = process.argv.slice(2).filter((arg) => arg !== "--pack-dry-run");

if (unknownArgs.length > 0) {
  console.error(`Unknown argument(s): ${unknownArgs.join(", ")}`);
  process.exit(1);
}

let normalizedVersion;
try {
  normalizedVersion = normalizeVersion(VERSION);
} catch (err) {
  console.error(err.message);
  process.exit(1);
}

const version = normalizedVersion.npmVersion;
const rootPackagePath = path.join(ROOT, "package.json");
const originalRootPackage = fs.readFileSync(rootPackagePath, "utf8");
const rootPackage = JSON.parse(originalRootPackage);
const platforms = rootPackage.customerioCli?.platforms || [];
const repository = { type: "git", url: "git+https://github.com/customerio/cli.git" };

function fail(message) {
  console.error(message);
  process.exit(1);
}

if (rootPackage.name !== "@customerio/cli") {
  fail(`root package must be @customerio/cli, found ${rootPackage.name}`);
}
if (!Array.isArray(platforms) || platforms.length === 0) {
  fail("package.json must define customerioCli.platforms");
}
if (!fs.existsSync(DIST)) {
  fail("dist/ does not exist; run GoReleaser before preparing npm packages");
}

const packageBase = rootPackage.name.replace(/^@[^/]+\//, "");

function walk(dir, visit) {
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      walk(full, visit);
    } else {
      visit(full);
    }
  }
}

function findBinary(platform) {
  const binary = `cio${platform.ext}`;
  const matches = [];

  walk(DIST, (file) => {
    const normalized = file.split(path.sep).join("/");
    if (
      path.basename(file) === binary &&
      normalized.includes(`_${platform.goos}_`) &&
      normalized.includes(`_${platform.goarch}`)
    ) {
      matches.push(file);
    }
  });

  if (matches.length !== 1) {
    throw new Error(
      `expected one binary for ${platform.npm}, found ${matches.length}: ${matches.join(", ")}`
    );
  }

  return matches[0];
}

function packDryRunPackage(packageDir) {
  if (packDryRun) {
    execFileSync("npm", ["pack", "--dry-run", "--json"], {
      cwd: packageDir,
      stdio: "inherit",
    });
  }
}

fs.rmSync(NPM_DIR, { recursive: true, force: true });

try {
  for (const platform of platforms) {
    const packageDir = path.join(NPM_DIR, `${packageBase}-${platform.npm}`);
    const packageName = `${rootPackage.name}-${platform.npm}`;

    fs.mkdirSync(path.join(packageDir, "bin"), { recursive: true });
    fs.writeFileSync(
      path.join(packageDir, "package.json"),
      JSON.stringify(
        {
          name: packageName,
          version,
          description: `Customer.io CLI ${platform.npm} binary`,
          repository,
          license: rootPackage.license,
          os: [platform.os],
          cpu: [platform.cpu],
          files: ["bin/", "LICENSE"],
          publishConfig: rootPackage.publishConfig,
        },
        null,
        2
      ) + "\n"
    );

    if (fs.existsSync(path.join(ROOT, "LICENSE"))) {
      fs.copyFileSync(path.join(ROOT, "LICENSE"), path.join(packageDir, "LICENSE"));
    }

    const binaryName = `cio${platform.ext}`;
    const binaryPath = path.join(packageDir, "bin", binaryName);
    fs.copyFileSync(findBinary(platform), binaryPath);
    fs.chmodSync(binaryPath, 0o755);

    packDryRunPackage(packageDir);
  }

  rootPackage.version = version;
  rootPackage.repository = repository;
  rootPackage.optionalDependencies = Object.fromEntries(
    platforms.map((platform) => [`${rootPackage.name}-${platform.npm}`, version])
  );
  rootPackage.publishConfig = rootPackage.publishConfig || {
    access: "public",
    provenance: true,
  };
  fs.writeFileSync(rootPackagePath, JSON.stringify(rootPackage, null, 2) + "\n");

  packDryRunPackage(ROOT);
} catch (err) {
  fs.writeFileSync(rootPackagePath, originalRootPackage);
  throw err;
}
