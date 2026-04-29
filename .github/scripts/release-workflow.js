#!/usr/bin/env node

const { execFileSync } = require("child_process");
const { normalizeVersion } = require("./release-version");

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
  };
}

function validateDispatch(env = process.env) {
  const ctx = releaseContext(env);

  if (ctx.dryRun) {
    if (ctx.resumeExistingNpm) {
      fail("resume_existing_npm is only valid for real npm publish recovery");
    }
    if (ctx.githubRef !== "refs/heads/main") {
      fail("release dry-run must be dispatched from refs/heads/main");
    }
    return ctx;
  }

  if (ctx.resumeExistingNpm) {
    if (ctx.githubRef !== ctx.tagRef) {
      fail(`npm publish recovery must be dispatched from ${ctx.tagRef}`);
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

  if (ctx.dryRun || ctx.resumeExistingNpm || ctx.githubRef !== "refs/heads/main") {
    fail("tag-and-dispatch is only valid for real releases dispatched from refs/heads/main");
  }
  if (!ctx.githubRepository) {
    fail("GITHUB_REPOSITORY must be set");
  }

  assertCheckoutSha(ctx);
  assertOriginMainSha(ctx);
  assertLocalTagDoesNotExist(ctx.tag);
  if (remoteTagExists(ctx.tag)) {
    fail(`tag ${ctx.tag} already exists on origin`);
  }

  run("git", ["tag", ctx.tag, ctx.githubSha]);
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

  assertCheckoutSha(ctx);
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
    fail(`resume_existing_npm requires an existing non-draft GitHub Release for ${ctx.tag}`);
  }

  if (release.tagName !== ctx.tag) {
    fail(`GitHub Release tag ${release.tagName} does not match ${ctx.tag}`);
  }
  if (release.isDraft) {
    fail(`GitHub Release ${ctx.tag} is still a draft`);
  }

  console.log(`Resuming npm publish after existing GitHub Release: ${release.url}`);
  return ctx;
}

function main(argv = process.argv.slice(2)) {
  const command = argv[0];
  if (!command || argv.length !== 1) {
    fail("usage: release-workflow.js <validate-dispatch|assert-dispatch-checkout|tag-and-dispatch|assert-tag-run|assert-existing-release>");
  }

  switch (command) {
    case "validate-dispatch":
      validateDispatch();
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
  validateDispatch,
};
