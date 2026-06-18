#!/usr/bin/env node

const { spawn } = require("child_process");
const { existsSync } = require("fs");
const path = require("path");

const pkgRoot = path.join(__dirname, "..");
const rootPkg = require(path.join(pkgRoot, "package.json"));
const optionalDependencies = rootPkg.optionalDependencies || {};
const platforms = rootPkg.customerioCli?.platforms || [];
const platform = platforms.find(
  (candidate) => candidate.os === process.platform && candidate.cpu === process.arch
);

if (!platform) {
  console.log("[cio postinstall] No matching platform found for", process.platform, process.arch);
  process.exit(0);
}

const platformPackage = Object.keys(optionalDependencies).find((packageName) =>
  packageName.endsWith(`-${platform.npm}`)
);

if (!platformPackage) {
  console.log("[cio postinstall] No platform package found for", platform.npm);
  process.exit(0);
}

let platformPackageRoot;
try {
  platformPackageRoot = path.dirname(
    require.resolve(`${platformPackage}/package.json`, { paths: [pkgRoot] })
  );
} catch {
  console.log("[cio postinstall] Could not resolve", platformPackage);
  process.exit(0);
}

const binPath = path.join(platformPackageRoot, "bin", `cio${platform.ext || ""}`);

if (!existsSync(binPath)) {
  console.log("[cio postinstall] Binary not found at", binPath);
  process.exit(0);
}

const isGlobal = process.env.npm_config_global === "true";
const scopeFlag = isGlobal ? "--global" : "--project";
const spawnOpts = { stdio: "inherit" };

// For project installs, npm runs postinstall with cwd inside node_modules.
// INIT_CWD is the directory where the user ran npm install (the project root).
if (!isGlobal && process.env.INIT_CWD) {
  spawnOpts.cwd = process.env.INIT_CWD;
}

console.log(`[cio postinstall] Running: cio skills install ${scopeFlag}`);
const child = spawn(binPath, ["skills", "install", scopeFlag], spawnOpts);

child.on("error", (err) => {
  console.log("[cio postinstall] Failed to run cio skills install:", err.message);
  process.exit(0);
});

child.on("close", (code) => {
  if (code !== 0) {
    console.log("[cio postinstall] cio skills install exited with code", code);
  }
  process.exit(0);
});
