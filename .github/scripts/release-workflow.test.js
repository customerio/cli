#!/usr/bin/env node

const assert = require("assert");
const { normalizeVersion } = require("./release-version");
const { validateDispatch } = require("./release-workflow");

function env(overrides) {
  return {
    VERSION_INPUT: "1.2.3",
    DRY_RUN: "false",
    RESUME_EXISTING_NPM: "false",
    GITHUB_REF: "refs/heads/main",
    GITHUB_REPOSITORY: "customerio/cli",
    GITHUB_SHA: "abc123",
    ...overrides,
  };
}

assert.deepStrictEqual(normalizeVersion("1.2.3"), {
  npmVersion: "1.2.3",
  tag: "v1.2.3",
  tagRef: "refs/tags/v1.2.3",
});
assert.deepStrictEqual(normalizeVersion("v1.2.3"), {
  npmVersion: "1.2.3",
  tag: "v1.2.3",
  tagRef: "refs/tags/v1.2.3",
});

for (const version of ["v1", "1.2", "version=foo", "1.2.3-beta.1", "1.2.3+build.1"]) {
  assert.throws(() => normalizeVersion(version), /version must use/);
}

assert.doesNotThrow(() =>
  validateDispatch(env({
    DRY_RUN: "true",
    GITHUB_REF: "refs/heads/main",
  }))
);
assert.throws(
  () => validateDispatch(env({ DRY_RUN: "true", RESUME_EXISTING_NPM: "true" })),
  /resume_existing_npm/
);
assert.throws(
  () => validateDispatch(env({ DRY_RUN: "true", GITHUB_REF: "refs/tags/v1.2.3" })),
  /dry-run must be dispatched/
);

assert.doesNotThrow(() =>
  validateDispatch(env({
    DRY_RUN: "false",
    RESUME_EXISTING_NPM: "false",
    GITHUB_REF: "refs/heads/main",
  }))
);
assert.doesNotThrow(() =>
  validateDispatch(env({
    VERSION_INPUT: "v1.2.3",
    DRY_RUN: "false",
    RESUME_EXISTING_NPM: "false",
    GITHUB_REF: "refs/tags/v1.2.3",
  }))
);
assert.throws(
  () => validateDispatch(env({ GITHUB_REF: "refs/tags/v1.2.4" })),
  /real release must be dispatched/
);

assert.doesNotThrow(() =>
  validateDispatch(env({
    DRY_RUN: "false",
    RESUME_EXISTING_NPM: "true",
    GITHUB_REF: "refs/tags/v1.2.3",
  }))
);
assert.throws(
  () => validateDispatch(env({ RESUME_EXISTING_NPM: "true", GITHUB_REF: "refs/heads/main" })),
  /recovery must be dispatched/
);

console.log("release-workflow tests passed");
