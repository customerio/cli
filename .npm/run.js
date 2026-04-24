#!/usr/bin/env node

const { spawnSync } = require("child_process");
const path = require("path");
const fs = require("fs");

const ext = process.platform === "win32" ? ".exe" : "";
const binPath = path.join(__dirname, "..", "bin", `cio${ext}`);

if (!fs.existsSync(binPath)) {
  console.error(
    `cio binary not found at ${binPath}\n` +
      "Run 'npm rebuild @customerio/cio-cli' or reinstall."
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

process.exit(result.status ?? 1);
