---
skill: auth
domain: Authentication, token management, OAuth exchange, credential storage
---

# Auth

The Customer.io CLI (`cio`) authenticates using service account tokens (`sa_live_...`) exchanged for short-lived JWTs via OAuth 2.0 client credentials grant against the UI API at `fly.customer.io`.

## Invariants

- Tokens are stored in `~/.cio/config.json` with `0600` permissions.
- Token resolution: `--token` flag → `CIO_TOKEN` env var → config file.
- `sa_live_` tokens CANNOT be used directly as Bearer tokens. They must be exchanged.
- The exchange endpoint is `POST /v1/service_accounts/oauth/token` (unauthenticated, rate-limited by IP).
- The exchange returns a short-lived JWT (`access_token`) with an `expires_in` field.
- The CLI caches the JWT and re-exchanges automatically when it expires (with 60s buffer).
- Two data centers: US (`https://us.fly.customer.io`) and EU (`https://eu.fly.customer.io`).
- Region is stored in config and determines the base URL for all requests.
- `auth login` and `auth logout` do NOT require an existing valid token.
- `auth status` exchanges the token to verify it works.
- `auth token` prints the raw `sa_live_` token to stdout (not the JWT).
- `auth signup start` and `auth signup verify` run unauthenticated; `--token` / `CIO_TOKEN` are ignored.
- `auth signup verify` persists the returned `sa_live_` bootstrap token + `account_id` to `~/.cio/config.json` on success — no separate `auth login` needed afterward.

## OAuth Exchange Details

```
POST /v1/service_accounts/oauth/token
Content-Type: application/x-www-form-urlencoded

grant_type=client_credentials&client_secret=sa_live_...

Response:
{"access_token":"<jwt>","token_type":"Bearer","expires_in":3600}
```

Alternatively, credentials can be sent via HTTP Basic auth (RFC 6749 §2.3.1):
```
Authorization: Basic base64(<service_account_id>:<sa_live_...>)
```

The `client_id` is optional. If provided, it must match the service account ID.

## Common Workflows

### First-time setup

```bash
# Interactive — prints the browser login URL, then prompts for the minted token
cio auth login

# Verify it works
cio auth status
```

### CI / automation setup

```bash
# Read token from stdin (no TTY needed; login still auto-discovers region)
echo "$CIO_TOKEN" | cio auth login --with-token

# Or just use env vars directly — in that case also provide region
CIO_TOKEN=sa_live_xxx CIO_REGION=eu cio auth status
```

### Check what's active

```bash
# Shows source, masked token, region, and verifies against API
cio auth status

# Just the raw sa_live_ token
cio auth token
```

### Switch regions

```bash
# Point direct token-based API calls at EU
CIO_TOKEN=sa_live_xxx CIO_REGION=eu cio api /v1/environments/{environment_id}/campaigns --params '{"environment_id":"123"}'
```

### Remove credentials

```bash
cio auth logout
```

### Provision a brand-new account (agentic signup)

A 2-step unauthenticated flow that stands up a new Customer.io account and
returns an Admin-scoped `sa_live_` bootstrap token. Use this when the agent
has no existing credentials. Both subcommands honor `--api-url` (defaults
to US) and `--dry-run`.

```bash
# Step 1 — email a 6-digit verification code
cio auth signup start --json '{"email":"agent+demo@example.com"}'

# Step 2 — verify the code and create the account
cio auth signup verify --json '{
  "email": "agent+demo@example.com",
  "code": "123456",
  "company_name": "Acme",
  "first_name": "Ada",
  "last_name": "Lovelace",
  "data_center": "us"
}'
```

On success, `verify` writes the returned `sa_live_` bootstrap token and
`account_id` to `~/.cio/config.json`, so the next `cio api ...` call is
already authenticated. The full response (including `token`) is still
printed once to stdout; the server will not return it again.

Target a different data center with `--api-url`, e.g.
`--api-url https://eu.fly.customer.io`.

## Troubleshooting

### "sa_live_ credentials cannot be used directly"

This means something is trying to use the raw token as a Bearer header. The CLI handles the exchange automatically — this error should not appear in normal usage. If it does, the OAuth exchange is being bypassed.

### "token exchange failed"

```bash
# Check region is correct
cio auth status

# Override the API region for direct token-based usage
CIO_TOKEN=sa_live_xxx CIO_REGION=eu cio auth status
```

### JWT expired mid-session

The CLI caches JWTs and auto-refreshes 60 seconds before expiry. On unexpected 401s (clock skew, race conditions), it automatically clears the token and retries once with a fresh JWT. Manual intervention should not be needed.

If problems persist:

```bash
# Force re-exchange by logging in again
cio auth login
```

### Wrong data center

If your account is in EU but you're hitting US (or vice versa), API calls will fail. Check and fix:

```bash
cio auth status  # shows region
CIO_TOKEN=sa_live_xxx CIO_REGION=eu cio auth status  # override region for direct token usage
```
