---
name: cio
description: >
  Customer.io CLI - use for Customer.io, Journeys, CDP Pipelines, builder
  onboarding, sandbox testing, app integration, SDK setup, transactional
  messaging, billing activation, going live, service account tokens, sources,
  destinations, identify/track events, and fly.customer.io / cdp.customer.io
  errors. Trigger even when the user does not name the CLI.
---

# Customer.io (`cio`)

This is the installable bootstrap skill for agents using the Customer.io CLI.
Run `cio prime` first; it prints the compact CLI reference, JSON output rules,
command syntax, schema introspection, pagination, error shapes, and
skill-reading guidance.

```bash
cio prime
```

If `cio` is not installed, fetch current installation instructions from the
README:

```bash
curl -fsSL https://raw.githubusercontent.com/customerio/cli/main/README.md
```

Use runtime references instead of copying large playbooks into this skill:

- `cio schema` for commands, flags, generic API routes, and payload shapes.
- `cio skills` to list currently available Customer.io reference skills.
- `cio skills read cli` for skills unique to the CLI (auth/login, integration, onboarding/new user setup); read it for the full index.
- `cio skills read <skill>` for the specific service-hosted reference needed.
- `cio api <path>` for API coverage that is not wrapped by a bespoke command.

Common routing:

- Builder/startup setup, signup, sandbox, first app integration,
  transactional proof, billing activation checks, and go-live start with
  `cio prime` and `cio skills read cli`.
- Existing Journeys resources such as campaigns, segments, templates,
  customers, newsletters, and subscription topics should use
  `cio skills read fly-api`.
- CDP Pipelines, sources, destinations, identify, track, page, and screen work
  should use `cio skills read cdp-api`.
- Design Studio email/component editing should use
  `cio skills read design-studio`.
- Analysis workflows, campaign review, goal-based segments, and Liquid should
  use `cio skills read recipes`.

Credential rule:

- Service account tokens (`sa_live_...`) authenticate the CLI and account-level
  management operations.
- Do not embed service account tokens in customer application code.
- Use CDP source write keys for SDK identify/track/page/screen ingestion.
- Use workspace-scoped App API keys for backend transactional sends.
