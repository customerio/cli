#!/usr/bin/env node

const assert = require("assert");
const { normalizeVersion, bumpVersion } = require("./release-version");
const { effectiveRef, validateDispatch, resolveVersion } = require("./release-workflow");

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
  prerelease: null,
  distTag: "latest",
});
assert.deepStrictEqual(normalizeVersion("v1.2.3"), {
  npmVersion: "1.2.3",
  tag: "v1.2.3",
  tagRef: "refs/tags/v1.2.3",
  prerelease: null,
  distTag: "latest",
});
assert.deepStrictEqual(normalizeVersion("1.2.3-alpha.1"), {
  npmVersion: "1.2.3-alpha.1",
  tag: "v1.2.3-alpha.1",
  tagRef: "refs/tags/v1.2.3-alpha.1",
  prerelease: "alpha.1",
  distTag: "alpha",
});
assert.deepStrictEqual(normalizeVersion("v1.2.3-beta.2"), {
  npmVersion: "1.2.3-beta.2",
  tag: "v1.2.3-beta.2",
  tagRef: "refs/tags/v1.2.3-beta.2",
  prerelease: "beta.2",
  distTag: "beta",
});
assert.deepStrictEqual(normalizeVersion("1.0.0-rc.1"), {
  npmVersion: "1.0.0-rc.1",
  tag: "v1.0.0-rc.1",
  tagRef: "refs/tags/v1.0.0-rc.1",
  prerelease: "rc.1",
  distTag: "rc",
});

for (const version of ["v1", "1.2", "version=foo", "1.2.3+build.1"]) {
  assert.throws(() => normalizeVersion(version), /version must use/);
}

assert.doesNotThrow(() =>
  validateDispatch(env({
    DRY_RUN: "true",
    GITHUB_REF: "refs/heads/main",
  }))
);
assert.doesNotThrow(() =>
  validateDispatch(env({
    DRY_RUN: "true",
    GITHUB_REF: "refs/heads/codex/publish-github-packages",
  }))
);
assert.throws(
  () => validateDispatch(env({ DRY_RUN: "true", RESUME_EXISTING_NPM: "true" })),
  /resume_existing_npm/
);
assert.throws(
  () => validateDispatch(env({ DRY_RUN: "true", GITHUB_REF: "refs/tags/v1.2.3" })),
  /dry-run must be dispatched from a branch/
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

// prerelease: allowed from any branch or matching tag
assert.doesNotThrow(() =>
  validateDispatch(env({
    VERSION_INPUT: "1.2.3-alpha.1",
    GITHUB_REF: "refs/heads/my-feature-branch",
  }))
);
assert.doesNotThrow(() =>
  validateDispatch(env({
    VERSION_INPUT: "1.2.3-alpha.1",
    GITHUB_REF: "refs/tags/v1.2.3-alpha.1",
  }))
);
assert.throws(
  () => validateDispatch(env({
    VERSION_INPUT: "1.2.3-alpha.1",
    GITHUB_REF: "refs/tags/v1.2.3-alpha.2",
  })),
  /prerelease must be dispatched/
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

// effectiveRef: returns refInput when set, otherwise branch from githubRef
assert.strictEqual(
  effectiveRef({ refInput: "my-branch", githubRef: "refs/heads/main" }),
  "my-branch"
);
assert.strictEqual(
  effectiveRef({ refInput: "", githubRef: "refs/heads/main" }),
  "main"
);
assert.strictEqual(
  effectiveRef({ refInput: "", githubRef: "refs/tags/v1.2.3" }),
  null
);

// ref input: stable version + non-main ref must fail
assert.throws(
  () => validateDispatch(env({ REF_INPUT: "my-branch" })),
  /stable releases require ref 'main'/
);
// ref input: stable version + main ref is fine
assert.doesNotThrow(() =>
  validateDispatch(env({ REF_INPUT: "main" }))
);
// ref input: stable version + empty ref (default) from main is fine
assert.doesNotThrow(() =>
  validateDispatch(env({ REF_INPUT: "" }))
);
// ref input: prerelease + non-main ref is fine
assert.doesNotThrow(() =>
  validateDispatch(env({
    VERSION_INPUT: "1.2.3-alpha.1",
    REF_INPUT: "my-branch",
  }))
);
// dispatch from non-main branch (via dropdown) without ref input: stable version must fail
assert.throws(
  () => validateDispatch(env({
    GITHUB_REF: "refs/heads/feature-branch",
    REF_INPUT: "",
  })),
  /stable releases require ref 'main'/
);
// dispatch from non-main branch (via dropdown) with prerelease: fine
assert.doesNotThrow(() =>
  validateDispatch(env({
    VERSION_INPUT: "1.2.3-alpha.1",
    GITHUB_REF: "refs/heads/feature-branch",
    REF_INPUT: "",
  }))
);

// bumpVersion: level handling and v-prefix tolerance
assert.strictEqual(bumpVersion("0.0.11", "patch"), "0.0.12");
assert.strictEqual(bumpVersion("v0.0.11", "patch"), "0.0.12");
assert.strictEqual(bumpVersion("1.2.3", "minor"), "1.3.0");
assert.strictEqual(bumpVersion("1.2.3", "major"), "2.0.0");
assert.strictEqual(bumpVersion("1.2.3"), "1.2.4"); // default patch
for (const bad of ["1.2", "v1", "1.2.3-beta.1", ""]) {
  assert.throws(() => bumpVersion(bad, "patch"), /base version must use/);
}
assert.throws(() => bumpVersion("1.2.3", "huge"), /bump must be patch/);

// resolveVersion: explicit input is an override (no git access needed)
assert.deepStrictEqual(resolveVersion({ VERSION_INPUT: "v1.2.3" }), {
  npmVersion: "1.2.3",
  tag: "v1.2.3",
  tagRef: "refs/tags/v1.2.3",
  prerelease: null,
  distTag: "latest",
});
assert.deepStrictEqual(resolveVersion({ VERSION_INPUT: "1.0.0-alpha.1" }), {
  npmVersion: "1.0.0-alpha.1",
  tag: "v1.0.0-alpha.1",
  tagRef: "refs/tags/v1.0.0-alpha.1",
  prerelease: "alpha.1",
  distTag: "alpha",
});

console.log("release-workflow tests passed");
