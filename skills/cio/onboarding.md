# Onboarding: Sign up, set up a domain, send your first email

Guided interactive onboarding for Customer.io. The goal is always to **send a test email** — that's the finish line. Ask before acting, confirm each step succeeds before moving on.

## Project context

If you have access to the user's project codebase (e.g. they invoked this from a repo directory), use it to personalize the experience from the start:

- **Company name**: infer from `package.json`, `setup.py`, `go.mod`, repo name, or similar metadata
- **Sending domain**: infer from the project's domain, homepage URL, or email references in the code
- **User name/email**: infer from git config (`user.name`, `user.email`) or environment

Present these as suggestions and **wait for the user to confirm or override before proceeding**. Never use inferred values to call a command without explicit confirmation first.

## Before you start

1. Confirm `cio` is installed and on PATH:
   ```bash
   command -v cio
   ```
   If missing, fetch install instructions: `curl -fsSL https://raw.githubusercontent.com/customerio/cli/main/README.md`. After install, re-run `command -v cio` to confirm the binary is on PATH.

2. Load the full CLI reference:
   ```bash
   cio prime
   ```

3. Check where the user is and skip completed steps (tell the user what you're skipping and why):
   - `cio auth status` returns `"verified": true` -> skip to Step 2
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
  "data_center": "us"
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

The response includes a `domain_connect_url` — ignore it. Always use `cio domains configure` (next step) for DNS setup, which goes through Entri and supports link tracking.

### 3b. Configure DNS via Entri

```bash
cio domains --env-id <environment_id> configure <domain>
```

Prints a URL for the Entri DNS setup flow (auto-configures MX, SPF, DKIM, DMARC). Tell the user to open it.

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

Default recipient: the user's own email from Step 1.

**Note:** The send command uses `--environment-id` (not `--env-id` like domain commands). The `--from` flag uses RFC 5322 format: `"Display Name <email@domain>"`.

```bash
cio send email --environment-id <environment_id> \
  --to <recipient-email> \
  --from "Acme <hello@example.com>" \
  --subject "Hello from Customer.io" \
  --body "<h1>It works!</h1><p>Your Customer.io account is set up and ready to go.</p>"
```

Personalize subject/body with the company name. Email may land in spam until DNS records are verified.

---

## Done

Now help the user integrate Customer.io into their app:

- **In-app messages**: identify where to add the web or mobile SDK for in-app messaging
- **Push notifications**: set up their mobile app for push via the mobile SDK
- **Profile and event data**: instrument their app to send identify/track calls so they can send personalized emails
- **Email templates**: help them build a branded email template (if a branding skill exists, hand off to it)

If you don't have access to a project codebase, ask the user if they'd like help integrating Customer.io into their app, and where the project is.
