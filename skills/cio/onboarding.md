# Onboarding: Sign up, set up a domain, send your first email

Guided interactive onboarding for Customer.io. The goal is always to **send a test email** — that's the finish line. Ask before acting, confirm each step succeeds before moving on.

## Project context

If you have access to the user's project codebase (e.g. they invoked this from a repo directory), use it to personalize the experience from the start:

- **Company name**: infer from `package.json`, `setup.py`, `go.mod`, repo name, or similar metadata
- **Sending domain**: infer from the project's domain, homepage URL, or email references in the code
- **User name/email**: infer from git config (`user.name`, `user.email`) or environment

Present these as suggestions and **wait for the user to confirm or override before proceeding**. Never use inferred values to call a command without explicit confirmation first.

## Before you start

1. Load the full CLI reference:
   ```bash
   cio prime
   ```

2. Check where the user is and skip completed steps (tell the user what you're skipping and why):
   - `cio auth status` returns `"verified": true` -> skip to Step 2; note the authenticated email for Step 5
   - Already knows their environment ID -> skip to Step 3
   - Already has a verified sending domain -> skip to Step 4

---

## Step 1 -- Authenticate

Determine if the user has an existing account or needs a new one.

### Path A: New account (signup)

**1a. Collect signup details** -- before calling any command, confirm the following with the user:

- **Email address** to sign up with (suggest from project context if available, but ask)
- **New account or existing?** — make sure the user actually wants to create a new account

Only proceed to 1b after the user confirms.

**1b. Start signup** -- emails a 6-digit verification code:

```bash
cio auth signup start --json '{"email":"<user-email>"}'
```

If the email is already in use (4xx), switch to Path B. If you get a 429 (rate limited), too many signups were attempted recently — tell the user to wait a few minutes and try again. If the verification email never arrived, wait, check spam, retry.

**1c. Verify** -- creates the account and returns a bootstrap token:

```bash
cio auth signup verify --json '{
  "email": "<user-email>",
  "code": "<6-digit-code>",
  "company_name": "<company>",
  "first_name": "<first>",
  "last_name": "<last>",
  "data_center": "<us-or-eu>"
}'
```

Ask the user which data center (US or EU). Default to US only if they have no preference. On success, credentials are saved to `~/.cio/config.json`.

### Path B: Existing account (login)

```bash
cio auth login
```

Prints a URL. User opens it, logs in, copies the `sa_live_...` token, pastes it back. CLI stores it and auto-discovers the data center. Alternatively:

```bash
echo "sa_live_..." | cio auth login --with-token
```

> **Note on tokens:** the `sa_live_` token authenticates the CLI itself (account-level, used internally for `cio api`, `cio send`, etc.). It should **not** be embedded in the user's production backend code. For backend integrations, issue a workspace-scoped App API key — see [`integration.md`](integration.md) Step 6.

### Verify auth

```bash
cio auth status
```

If 401: token may be invalid or expired -- retry `cio auth login`.

---

## Step 2 -- Find the workspace

```bash
cio api /v1/accounts/{account_id}/environments --jq '.environments[] | {id, name}'
```

New accounts typically have one workspace. If multiple, pick the first one that's ready to send (has a verified domain + from address). Only ask the user to choose if none are ready or you need to decide which to set up.

### Check existing setup (Path B only)

For existing accounts, check what's already configured:

```bash
cio domains --env-id <environment_id> list --jq '.domains[] | {id, domain, verified}'
cio domains --env-id <environment_id> from_addresses list --jq '.identities[] | {id, name, email}'
```

- Verified domain + from address -> go directly to Step 5 and send the test email. Do not present a menu of options. The user asked to build with Customer.io — sending is the goal.
- Domain but no from address -> skip to Step 4
- Domain with unverified DNS -> offer `cio domains --env-id <environment_id> configure <domain> --cname email`, or skip to Step 4

---

## Step 3 -- Add and configure a sending domain

Ask which domain they'll send from. Suggest a domain from the company name if known.

### 3a. Add the domain

```bash
cio domains --env-id <environment_id> add <domain>
```

The response includes a `domain_connect_url` — ignore it. Always use `cio domains configure` (next step) for the one-click DNS setup flow, which supports link tracking.

### 3b. Configure DNS with one-click setup

```bash
cio domains --env-id <environment_id> configure <domain>
```

Prints a URL for the one-click DNS setup flow (auto-configures MX, SPF, DKIM, DMARC). Tell the user to open it.

**Link tracking (optional):** Only pass `--cname` if the user specifically asks for link tracking. When used, it requires a subdomain value (e.g. `--cname email` for `email.<domain>`, or `--cname track` for `track.<domain>`). Do not pass `--cname` by default.

### 3c. Verify DNS records

```bash
cio domains --env-id <environment_id> verify <domain>
```

If records are failing, show what's missing. DNS propagation may take hours -- proceed and re-run verify later.

---

## Step 4 -- Add a from address

Use the company name as display name and `hello@<domain>` as the email. Confirm with the user before creating.

```bash
cio domains --env-id <environment_id> from_addresses add \
  --name "<Display Name>" \
  --email "<sender@domain>"
```

---

## Step 5 -- Send a test email

Send a test email to the email the user signed up with or authenticated as — it is automatically opted in.

**Note:** The send command uses `--environment-id` (not `--env-id` like domain commands). Pass `--from` as a bare email address with no display name.

Compose a polished HTML body — clean typography, a centered card, friendly tone. Personalize the subject and body with the company name when known. Don't reuse the literal example below verbatim; treat it as a baseline and write something that feels designed, not debug output.

```bash
cio send email --environment-id <environment_id> \
  --to <authenticated-user-email> \
  --from <sender@domain> \
  --subject "Welcome to Customer.io" \
  --body '<div style="font-family:-apple-system,BlinkMacSystemFont,Segoe UI,Roboto,sans-serif;max-width:480px;margin:40px auto;padding:40px 32px;background:#ffffff;border:1px solid #eaeaea;border-radius:12px;text-align:center;color:#1d1d1f;"><h1 style="margin:0 0 16px;font-size:24px;font-weight:600;">You are all set 🎉</h1><p style="margin:0 0 12px;font-size:16px;line-height:1.5;color:#424245;">Your Customer.io account is configured and your sending domain is live.</p><p style="margin:0 0 24px;font-size:16px;line-height:1.5;color:#424245;">This is the first email from your new setup — nice work.</p><p style="margin:0;font-size:13px;color:#86868b;">— The Customer.io team</p></div>' \
  --watch
```

Email may land in spam until DNS records are verified.

---

## Step 6 -- Explain go-live costs when the user is ready

The onboarding finish line is still a test email. If the user asks about
launching, sending to real customers, plan limits, or go-live costs, follow
[billing.md](billing.md).

---

## Done — what to suggest next

**Do not present a menu of Customer.io features as next steps.** Do not
list Journeys, Campaigns, Broadcasts, Segments, In-app, Push, SMS,
Pipelines, or SDK integration as options for the user to pick from
unless you have already checked the user's plan and confirmed each one
is included on it. Listing them and then offering to "check what's
ready" is the same failure as listing them outright — it implies they
are all available.

The right flow is the inverse:

1. Ask the user what they want to build or do next, in their own terms
   (e.g. "send a welcome email when someone signs up", "send order
   receipts from my app"). Don't seed the answer with product names.
2. Once you know the goal, follow [billing.md](billing.md) to check the
   account's plan and confirm — from live sources, not memory — whether
   the plan supports that goal.
3. If the plan supports it, help them build it. If it doesn't, say so
   plainly and link the upgrade path from [billing.md](billing.md).
   Don't soften it into "might need an upgrade".

Do not characterize the user's plan from memory or training data. In
particular, do not say any of the following unless
[billing.md](billing.md) and the live source confirm it for the account:

- That **Builder** is a free tier, sandbox, trial, or testing-only plan.
  It is a real plan with its own go-live path.
- That the user must **upgrade to a paid plan** to send to real
  customers, go live, or use Customer.io for production.
- That a specific feature is included or excluded on the user's plan.

If the user has a project codebase open, integration help that fits
their plan and goal is usually a strong next step; otherwise ask where
the project is before suggesting integration work.
[integration.md](integration.md) covers SDK and transactional-from-code
paths.
