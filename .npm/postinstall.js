#!/usr/bin/env node

const { execFileSync } = require("child_process");
const fs = require("fs");
const path = require("path");

const PLATFORM_PACKAGES = {
  "darwin-arm64": "@customerio/cio-cli-darwin-arm64",
  "darwin-x64": "@customerio/cio-cli-darwin-x64",
  "linux-arm64": "@customerio/cio-cli-linux-arm64",
  "linux-x64": "@customerio/cio-cli-linux-x64",
};

const ext = process.platform === "win32" ? ".exe" : "";
const destDir = path.join(__dirname, "..", "bin");
// Use the platform-appropriate name (cio on Unix, cio.exe on Windows).
// The bin entry in package.json points at scripts/run.js which resolves the
// correct binary name at runtime.
const destPath = path.join(destDir, `cio${ext}`);

// Try platform binary package first (published npm install flow).
if (tryPlatformPackage()) {
  process.exit(0);
}

// Fall back to building from Go source when a platform package is unavailable.
if (tryGoBuild()) {
  process.exit(0);
}

console.error(
  "Failed to install cio CLI: no platform package found and no explicit source checkout could build the binary.\n" +
    "Install from npm on a supported platform, or set CIO_CLI_SOURCE to a local cio-cli source checkout with Go installed."
);
process.exit(1);

// --- Strategies ---

function tryPlatformPackage() {
  const platformKey = `${process.platform}-${process.arch}`;
  const pkg = PLATFORM_PACKAGES[platformKey];

  if (!pkg) return false;

  let binPath;
  try {
    const pkgDir = path.dirname(require.resolve(`${pkg}/package.json`));
    binPath = path.join(pkgDir, "bin", `cio${ext}`);
  } catch {
    return false;
  }

  if (!fs.existsSync(binPath)) return false;

  console.log(`Found platform package ${pkg}`);
  linkBinary(binPath);
  return true;
}

function tryGoBuild() {
  // Published npm packages do not include Go source. CIO_CLI_SOURCE is an
  // explicit development escape hatch; the package root supports repo checkouts.
  const pkgRoot = path.join(__dirname, "..");
  const candidates = [
    process.env.CIO_CLI_SOURCE,
    pkgRoot,                                     // repo root (package.json is here)
  ].filter(Boolean);

  const goRoot = candidates.find(
    (dir) => fs.existsSync(path.join(dir, "go.mod")) &&
             fs.existsSync(path.join(dir, "main.go"))
  );

  if (!goRoot) {
    console.log("No Go source found, skipping Go build. Set CIO_CLI_SOURCE to override.");
    return false;
  }

  // Check that Go is available
  try {
    execFileSync("go", ["version"], { stdio: "ignore" });
  } catch {
    console.log("Go not found on PATH, skipping Go build.");
    return false;
  }

  console.log(`Building from Go source at ${goRoot}...`);

  fs.mkdirSync(destDir, { recursive: true });

  try {
    execFileSync("go", ["build", "-o", destPath, "."], {
      cwd: goRoot,
      stdio: "inherit",
    });
    fs.chmodSync(destPath, 0o755);
    console.log("Go build succeeded.");
    return true;
  } catch (err) {
    console.error("Go build failed:", err.message);
    return false;
  }
}

function linkBinary(binPath) {
  fs.mkdirSync(destDir, { recursive: true });

  try {
    fs.unlinkSync(destPath);
  } catch {}

  try {
    fs.symlinkSync(binPath, destPath);
  } catch {
    // Fallback to copy if symlink fails (e.g. Windows without dev mode)
    fs.copyFileSync(binPath, destPath);
  }

  fs.chmodSync(destPath, 0o755);
}
