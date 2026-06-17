#!/usr/bin/env node

const { execFileSync } = require("child_process");
const fs = require("fs");
const { normalizeVersion, bumpVersion } = require("./release-version");

function fail(message) {
  throw new Error(message);
}

function inputBool(value, name) {
  if (value === "true" || value === true) {
    return true;
  }
  if (value === "false" || value === false) {
    return false;
  }
  fail(`${name} must be true or false`);
}

function releaseContext(env = process.env) {
  return {
    ...normalizeVersion(env.VERSION_INPUT),
    dryRun: inputBool(env.DRY_RUN, "DRY_RUN"),
    resumeExistingNpm: inputBool(env.RESUME_EXISTING_NPM, "RESUME_EXISTING_NPM"),
    githubRef: env.GITHUB_REF || "",
    githubRepository: env.GITHUB_REPOSITORY || "",
    githubSha: env.GITHUB_SHA || "",
    refInput: env.REF_INPUT || "",
  };
}

function effectiveRef(ctx) {
  if (ctx.refInput) return ctx.refInput;
  const match = ctx.githubRef.match(/^refs\/heads\/(.+)$/);
  return match ? match[1] : null;
}

function validateDispatch(env = process.env) {
  const ctx = releaseContext(env);

  if (ctx.dryRun) {
    if (ctx.resumeExistingNpm) {
      fail("resume_existing_npm is only valid for real package publishing recovery");
    }
    if (!ctx.githubRef.startsWith("refs/heads/")) {
      fail("release dry-run must be dispatched from a branch");
    }
    return ctx;
  }

  if (ctx.resumeExistingNpm) {
    if (ctx.githubRef !== ctx.tagRef) {
      fail(`package publishing recovery must be dispatched from ${ctx.tagRef}`);
    }
    return ctx;
  }

  const ref = effectiveRef(ctx);
  if (!ctx.prerelease && ref && ref !== "main") {
    fail(`stable releases require ref 'main'; use a prerelease version for branch '${ref}'`);
  }

  if (ctx.prerelease) {
    if (!ctx.githubRef.startsWith("refs/heads/") && ctx.githubRef !== ctx.tagRef) {
      fail(`prerelease must be dispatched from a branch or ${ctx.tagRef}`);
    }
    return ctx;
  }

  if (ctx.githubRef !== "refs/heads/main" && ctx.githubRef !== ctx.tagRef) {
    fail(`real release must be dispatched from refs/heads/main or ${ctx.tagRef}`);
  }

  return ctx;
}

function run(command, args, options = {}) {
  return execFileSync(command, args, {
    stdio: "inherit",
    ...options,
  });
}

function read(command, args) {
  return execFileSync(command, args, {
    encoding: "utf8",
    stdio: ["ignore", "pipe", "pipe"],
  }).trim();
}

function latestTag() {
  try {
    run("git", ["fetch", "--tags", "--force", "origin"], { stdio: "ignore" });
  } catch (err) {
    // Tags may already be present from a full-depth checkout; fall back to local.
  }

  const tags = read("git", ["tag", "--list", "v*.*.*"])
    .split("\n")
    .map((tag) => tag.trim())
    .filter((tag) => /^v\d+\.\d+\.\d+$/.test(tag));

  if (!tags.length) {
    return null;
  }

  return tags
    .sort((a, b) => {
      const pa = a.slice(1).split(".").map(Number);
      const pb = b.slice(1).split(".").map(Number);
      return pa[0] - pb[0] || pa[1] - pb[1] || pa[2] - pb[2];
    })
    .pop();
}

function resolveVersion(env = process.env) {
  const input = String(env.VERSION_INPUT || "").trim();
  const resolved = input
    ? normalizeVersion(input)
    : normalizeVersion(bumpVersion((latestTag() || "v0.0.0").slice(1), env.BUMP || "patch"));

  if (env.GITHUB_OUTPUT) {
    fs.appendFileSync(env.GITHUB_OUTPUT, `version=${resolved.npmVersion}\n`);
  }
  console.log(`Resolved release version: ${resolved.tag}`);
  return resolved;
}

function assertCheckoutSha(ctx) {
  if (!ctx.githubSha) {
    fail("GITHUB_SHA must be set");
  }

  const head = read("git", ["rev-parse", "HEAD"]);
  if (head !== ctx.githubSha) {
    fail("checked-out commit does not match dispatch SHA");
  }
}

function assertOriginMainSha(ctx) {
  run("git", ["fetch", "--force", "origin", "refs/heads/main:refs/remotes/origin/main"]);
  const mainSha = read("git", ["rev-parse", "refs/remotes/origin/main"]);
  if (mainSha !== ctx.githubSha) {
    fail("origin/main no longer resolves to dispatch SHA");
  }
}

function assertTagSha(ctx) {
  run("git", ["fetch", "--force", "origin", `${ctx.tagRef}:${ctx.tagRef}`]);
  const tagSha = read("git", ["rev-list", "-n", "1", ctx.tagRef]);
  if (tagSha !== ctx.githubSha) {
    fail(`tag ${ctx.tag} no longer resolves to dispatch SHA`);
  }
}

function remoteTagExists(tag) {
  try {
    execFileSync("git", ["ls-remote", "--exit-code", "--tags", "origin", `refs/tags/${tag}`], {
      stdio: "ignore",
    });
    return true;
  } catch (err) {
    if (err.status === 2) {
      return false;
    }
    throw err;
  }
}

function assertLocalTagDoesNotExist(tag) {
  try {
    execFileSync("git", ["show-ref", "--verify", "--quiet", `refs/tags/${tag}`], {
      stdio: "ignore",
    });
    fail(`tag ${tag} already exists locally`);
  } catch (err) {
    if (err.status === 1) {
      return;
    }
    throw err;
  }
}

function tagAndDispatch(env = process.env) {
  const ctx = validateDispatch(env);

  if (ctx.dryRun || ctx.resumeExistingNpm) {
    fail("tag-and-dispatch is only valid for real releases dispatched from a branch");
  }
  if (!ctx.githubRef.startsWith("refs/heads/")) {
    fail("tag-and-dispatch must be dispatched from a branch");
  }
  if (!ctx.githubRepository) {
    fail("GITHUB_REPOSITORY must be set");
  }

  const isCustomRef = Boolean(ctx.refInput) && ctx.refInput !== "main";
  const commitSha = isCustomRef
    ? read("git", ["rev-parse", "HEAD"])
    : ctx.githubSha;

  if (!isCustomRef) {
    assertCheckoutSha(ctx);
    if (!ctx.prerelease) {
      assertOriginMainSha(ctx);
    }
  }
  assertLocalTagDoesNotExist(ctx.tag);
  if (remoteTagExists(ctx.tag)) {
    fail(`tag ${ctx.tag} already exists on origin`);
  }

  run("git", ["tag", ctx.tag, commitSha]);
  run("git", ["push", "origin", `refs/tags/${ctx.tag}`]);
  run("gh", [
    "workflow",
    "run",
    "release.yml",
    "--repo",
    ctx.githubRepository,
    "--ref",
    ctx.tag,
    "-f",
    `version=${ctx.tag}`,
    "-f",
    "dry_run=false",
    "-f",
    "resume_existing_npm=false",
  ]);
}

function assertDispatchCheckout(env = process.env) {
  const ctx = validateDispatch(env);

  if (!ctx.refInput || ctx.refInput === "main") {
    assertCheckoutSha(ctx);
  }
  return ctx;
}

function assertTagRun(env = process.env) {
  const ctx = validateDispatch(env);

  if (ctx.dryRun || ctx.githubRef !== ctx.tagRef) {
    fail(`release publishing must run from ${ctx.tagRef}`);
  }

  assertCheckoutSha(ctx);
  assertTagSha(ctx);
  return ctx;
}

function assertExistingRelease(env = process.env) {
  const ctx = releaseContext(env);

  if (!ctx.githubRepository) {
    fail("GITHUB_REPOSITORY must be set");
  }

  let release;
  try {
    release = JSON.parse(
      read("gh", [
        "release",
        "view",
        ctx.tag,
        "--repo",
        ctx.githubRepository,
        "--json",
        "isDraft,tagName,url",
      ])
    );
  } catch (err) {
    fail(`package publishing recovery requires an existing non-draft GitHub Release for ${ctx.tag}`);
  }

  if (release.tagName !== ctx.tag) {
    fail(`GitHub Release tag ${release.tagName} does not match ${ctx.tag}`);
  }
  if (release.isDraft) {
    fail(`GitHub Release ${ctx.tag} is still a draft`);
  }

  console.log(`Resuming package publishing after existing GitHub Release: ${release.url}`);
  return ctx;
}

function main(argv = process.argv.slice(2)) {
  const command = argv[0];
  if (!command || argv.length !== 1) {
    fail("usage: release-workflow.js <validate-dispatch|resolve-version|assert-dispatch-checkout|tag-and-dispatch|assert-tag-run|assert-existing-release>");
  }

  switch (command) {
    case "validate-dispatch":
      validateDispatch();
      break;
    case "resolve-version":
      resolveVersion();
      break;
    case "assert-dispatch-checkout":
      assertDispatchCheckout();
      break;
    case "tag-and-dispatch":
      tagAndDispatch();
      break;
    case "assert-tag-run":
      assertTagRun();
      break;
    case "assert-existing-release":
      assertExistingRelease();
      break;
    default:
      fail(`unknown command: ${command}`);
  }
}

if (require.main === module) {
  try {
    main();
  } catch (err) {
    console.error(err.message);
    process.exit(1);
  }
}

module.exports = {
  releaseContext,
  effectiveRef,
  validateDispatch,
  latestTag,
  resolveVersion,
};
