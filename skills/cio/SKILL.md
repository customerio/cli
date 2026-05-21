---
name: cio
description: >
  Customer.io CLI — use for Customer.io, Journeys, or CDP Pipelines tasks,
  including getting started phrases like "I want to build with Customer.io",
  onboarding, signup, sending a first email, campaigns, broadcasts, segments,
  people, environments, billing, pricing, plans, signing secrets, sources,
  destinations, track/identify events, `sa_live_` tokens, and
  `fly.customer.io` / `cdp.customer.io` errors, even when the user does not
  name the CLI.
---

# Customer.io (`cio`)

Run `cio prime` first. It dumps the full LLM-ready reference -- commands,
flags, schemas, pagination, error shapes -- so you don't have to guess or
spelunk through `--help`.

If `cio` is not installed, run `npm i -g @customerio/cli`. After installing,
run `cio prime`. If installation fails, fetch the README for other install
options:

```bash
curl -fsSL https://raw.githubusercontent.com/customerio/cli/main/README.md
```

Everything below handles setup cases you might hit.

## If the user wants to get started with Customer.io

Follow [onboarding.md](onboarding.md) when the intent is about
starting, building, trying, or setting up Customer.io — even if they
have a project open. Account creation, authentication, domain setup,
and sending a first email all live here. If the user isn't
authenticated yet, onboarding is almost certainly the right path.

Examples: "I want to build with Customer.io", "help me set up
Customer.io", "I want to try Customer.io", "set up a new account",
"send my first email".

## If the user asks about plans, pricing, billing, or going live

Follow [billing.md](billing.md) when the user asks what plan they are on,
whether they need to pay, how to launch, how to unlock external sends, or
anything about billing, subscriptions, credits, invoices, receipts,
payment methods, or plan limits. This includes billing questions that come up
during onboarding.

Examples: "do I need to purchase a plan to go live?", "what does my plan
include?", "how much does this cost?", "can I send to real customers?",
"upgrade my plan", "buy credits", "what plan am I on?".

## If the user wants to integrate Customer.io into their app

Follow [integration.md](integration.md) when the user wants to add,
integrate, install, wire, hook up, or connect Customer.io to an existing
app or codebase. This covers SDK install, identify/track instrumentation,
in-app messaging, push notifications, and transactional sends from app
code. The signal is "I have a project open and want Customer.io in it" —
the user does not need to name an SDK.

Examples: "add Customer.io to my app", "integrate Customer.io", "wire
Customer.io into this project", "install the Customer.io SDK", "send
transactional emails from my app", "track events from my backend",
"hook up identify calls".

## If the user wants to open the Customer.io UI

Run `cio auth login`. When the user is already authenticated, it prints
a one-click browser link — no password needed. If not authenticated, it
starts the interactive login flow (see auth error section below).

Examples: "open the UI", "log into Customer.io", "open the dashboard",
"take me to Customer.io".

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
