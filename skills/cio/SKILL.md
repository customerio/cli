---
name: cio
description: >
  Customer.io CLI — use for any Customer.io, Journeys, or CDP Pipelines task.
  Handles campaigns, broadcasts, segments, people, environments, subscription
  topics, signing secrets, sources, destinations, track/identify events,
  `sa_live_` tokens, and `fly.customer.io` / `cdp.customer.io` errors.
  Trigger even when the user doesn't name the CLI.
---

# Customer.io (`cio`)

Run `cio prime` first. It dumps the full LLM-ready reference — commands,
flags, schemas, pagination, error shapes — so you don't have to guess or
spelunk through `--help`.

```bash
cio prime
```

Everything below handles setup cases you might hit.

## If the user wants to build on Customer.io

If the user mentions customer.io/builders, wants to try Customer.io,
asks to set up a new account, configure email sending, or send their
first message — follow [onboarding.md](onboarding.md). It walks through
the full builder quickstart: sign up (or log in) → find workspace →
add sending domain → configure DNS → create from address → send a
test email.

## If the user wants to integrate an SDK

If the user wants to add Customer.io to their app, instrument
identify/track calls, set up in-app messaging, push notifications,
or connect their codebase to Customer.io — follow
[integration.md](integration.md). It walks through: detect tech
stack → create a CDP source → get credentials → install the SDK →
add identify/track calls → verify the integration.

## If `cio` isn't installed

If `command -v cio` returns nothing, fetch the current install options
from the README (source of truth — this skill doesn't list them so it
can't rot), show the user the exact command, and ask before running it:

```bash
curl -fsSL https://raw.githubusercontent.com/customerio/cli/main/README.md
```

## If a command fails with an auth error

`cio auth login` auto-discovers the data center, so don't pass a region
flag. Offer the user two paths:

- **User runs it themselves** (keeps the token out of chat):
  `cio auth login`
- **You run it** — only if they explicitly paste the token. Pipe via
  stdin so it doesn't hit shell history:
  `echo "$TOKEN" | cio auth login --with-token`

Service account tokens (`sa_live_...`) are at **Customer.io UI → Account
Settings → Manage API Credentials → Service Accounts**.
