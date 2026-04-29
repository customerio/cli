# Customer.io CLI (`cio`)

An agent-first CLI for Customer.io APIs.

**800+ Journeys routes + 100+ CDP Pipelines routes, zero per-endpoint code.** A single `cio api <path>` command covers every endpoint. Every command returns structured JSON to stdout. Every error returns structured JSON to stderr.

AI agents are the primary consumer. Use `cio schema` to introspect endpoints before calling them.

## Install

```bash
npm i -g @customerio/cli
cio --help
```

To build from source instead:

```bash
go install github.com/customerio/cli@latest
```

## Install the agent skill

This repo ships a [SKILL.md](skills/cio/SKILL.md) so Claude Code, Cursor, Codex, Windsurf, and other agents that support [open agent skills](https://github.com/vercel-labs/skills) know how to drive the CLI. Install it with:

```bash
npx skills add customerio/cli
```

## Authentication

The CLI uses **service account tokens** (`sa_live_...`) for authentication. These are exchanged for short-lived JWTs via OAuth 2.0 client credentials, just like `gh auth`.

### Login

```bash
# Interactive — prints the browser login URL, then prompts for the minted token
cio auth login

# Read from stdin (for CI/automation; login still auto-discovers region)
echo "$SA_TOKEN" | cio auth login --with-token

# Verify auth works
cio auth status

# Print raw token
cio auth token

# Logout
cio auth logout
```

### Token Resolution Order

1. `--token` flag (highest priority)
2. `CIO_TOKEN` environment variable
3. `~/.cio/config.json` file (lowest priority)

When you use `CIO_TOKEN` or `--token` directly on normal commands, you may
also need `CIO_REGION=us|eu` or `--api-url`.

### How It Works

1. You provide a `sa_live_...` token (from Customer.io UI → Account Settings → Manage API Credentials → Service Accounts)
2. The CLI exchanges it for a JWT via `POST /v1/service_accounts/oauth/token`
3. The JWT is cached locally and refreshed automatically when it expires
4. All API calls use `Authorization: Bearer <jwt>`

### Override per-command

```bash
cio --token sa_live_xxx api /v1/environments/{environment_id}/campaigns --params '{"environment_id": "123"}'
CIO_TOKEN=sa_live_xxx cio api /v1/environments/{environment_id}/campaigns --params '{"environment_id": "123"}'
```

## Usage

Use `cio api <path>` for any API endpoint. Path placeholders are resolved from `--params`. The HTTP method defaults to GET (or POST if `--json` is provided); override with `-X`:

```bash
# List campaigns in workspace 123
cio api /v1/environments/{environment_id}/campaigns --params '{"environment_id": "123"}'

# Get a specific campaign
cio api /v1/environments/{environment_id}/campaigns/{campaign_id} \
  --params '{"environment_id": "123", "campaign_id": "456"}'

# Create a campaign
cio api /v1/environments/{environment_id}/campaigns \
  --params '{"environment_id": "123"}' \
  --json '{"campaign": {"name": "Welcome Flow", "type": "none"}}'

# Explicit method override
cio api /v1/environments/{environment_id}/campaigns/{campaign_id} -X DELETE \
  --params '{"environment_id": "123", "campaign_id": "456"}'

# Introspect endpoints
cio schema                          # list all resources
cio schema campaigns                # list endpoints for a resource
cio schema campaigns.list           # full schema for a method
cio schema GET /v1/environments/{environment_id}/campaigns  # by HTTP method + path
```

### Account ID fallback

Paths that include `{account_id}` auto-fill from the account ID stored during `cio auth login`, so you don't need to pass it on every call:

```bash
cio api /v1/accounts/{account_id}/environments
```

Pass `--params '{"account_id": "..."}'` to override, or set `CIO_ACCESS_TOKEN` to disable the fallback (the pre-exchanged JWT may belong to a different account).

### Filtering with jq

```bash
# Filter with --jq to save context window
cio api /v1/environments/{environment_id}/campaigns \
  --params '{"environment_id": "123"}' \
  --jq '.campaigns[] | {id, name, state}'

# Complex filtering
cio api /v1/environments/{environment_id}/campaigns \
  --params '{"environment_id": "123"}' \
  --jq '.campaigns[] | select(.state == "active") | {id, name}'
```

### Write operations

```bash
# Always dry-run first
cio api /v1/environments/{environment_id}/campaigns \
  --params '{"environment_id": "123"}' \
  --json '{"campaign": {"name": "Welcome Flow", "type": "none"}}' --dry-run

# Then execute (removes --dry-run)
cio api /v1/environments/{environment_id}/campaigns \
  --params '{"environment_id": "123"}' \
  --json '{"campaign": {"name": "Welcome Flow", "type": "none"}}'
```

### Pagination

```bash
cio api /v1/environments/{environment_id}/campaigns \
  --params '{"environment_id": "123"}' --page 2 --limit 50

# Auto-paginate (emits NDJSON — one JSON object per line)
cio api /v1/environments/{environment_id}/campaigns \
  --params '{"environment_id": "123"}' --page-all
```

## Global Flags

| Flag | Env Var | Description |
|---|---|---|
| `--token <value>` | `CIO_TOKEN` | Service account token override |
| `-X, --method` | | HTTP method override (default: GET, or POST if --json) |
| `--json <payload>` | | Raw JSON request body or `@filename` to read from file |
| `--params <json>` | | Query parameters as JSON → query string |
| `--jq <expr>` | | jq expression filter (via gojq) |
| `--dry-run` | | Validate + print request, skip execution |
| `--api-url <url>` | | API base URL override |
| `--timeout <duration>` | `CIO_TIMEOUT` | HTTP request timeout (default: 30s) |
| `--page <n>` | | Page number |
| `--limit <n>` | | Page size |
| `--page-all` | | Auto-paginate, emit NDJSON |

## Exit Codes

| Code | Meaning |
|---|---|
| 0 | Success |
| 1 | General error |
| 2 | Validation / input error |
| 3 | Authentication error |
| 4 | Authorization error |
| 5 | API error (4xx/5xx) |

## Error Format

```json
{"error":true,"code":"AUTH_ERROR","message":"Not authenticated.","details":{"status_code":401}}
```

## Architecture

The CLI uses a **generic `api` command + route registry** architecture:

1. `cio api <path>` — a single command that takes any API path with `{placeholder}` params
2. OpenAPI specs are downloaded from the live API on first use and cached locally under `~/.cio/cache/specs/` (24h TTL, ETag-based conditional refresh). Use `cio schema --refresh` to force re-download.
3. `internal/routes/enrichment.json` — summaries, param descriptions, query params for routes not yet annotated in the OpenAPI spec

```bash
# Discover resources
cio schema

# List endpoints for a resource
cio schema campaigns

# Inspect a method's parameters
cio schema campaigns.list

# Make an API call
cio api /v1/environments/{environment_id}/campaigns --params '{"environment_id": "123"}'

# CDP Pipelines (workspace_id = environment_id)
cio api /cdp/api/workspaces/{workspace_id}/sources --params '{"workspace_id": "123"}'
cio schema sources
```

## Development

```bash
go build -o cio .
go test ./...
go test ./... -v -run TestAuthLogin
```

## License

Licensed under Apache License 2.0 with the Commons Clause Restriction. See [LICENSE](LICENSE).
