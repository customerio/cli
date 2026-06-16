You have access to `cio`, an agent-first CLI for Customer.io. Most commands return structured JSON to stdout, and errors return structured JSON to stderr. The `cio prime` command itself outputs Markdown for prompt/context injection.

## Key Rules

1. Unless you are intentionally using `cio prime`, assume command output is JSON. Parse it, don't regex it.
2. Path placeholders are passed as strings. Many IDs are numeric, but profile/object IDs can be string values such as `eea50d000102`; let the API own endpoint-specific ID semantics.
3. ALWAYS use `--jq` on read calls to limit output and save tokens. Add `-r` to print a raw string (an id, token, or body); use `--arg`/`--argjson` to build a request body with embedded values. The bundled gojq covers both, so you never need an external `jq`.
4. ALWAYS use `--dry-run` before any mutating call to preview what will be sent.
5. Read the relevant skill (`cio skills read`) BEFORE making complex API calls — the API has non-obvious required fields, multi-step workflows, and silent failures.

## Authentication

Auth uses service account tokens exchanged for JWTs. Check status with:

```
cio auth status
```

If auth fails or there is no token yet, read `cio skills read cli/auth.md`
and follow its login flow. Never have the user paste a token into the
conversation or pass one as a shell argument.

## Profiles

Credentials live in named profiles (in `~/.cio/config.json`), each with its own
token, region, and optional API base URL. This lets one machine hold several
accounts (e.g. production, staging, a client) without re-authenticating.

```bash
cio profile list                 # list profiles; the saved default is marked current
cio profile use <name>           # set the default profile
cio profile remove <name>        # delete a profile
cio auth login --profile <name>  # create/re-auth a profile (persists a custom --api-url)
```

Select a profile for a single command with the global `--profile <name>` flag,
or for a whole session with the `CIO_PROFILE` env var. Resolution order:
`--profile` → `CIO_PROFILE` → stored `current_profile` → `default`. A legacy
single-credential config is migrated to a `default` profile automatically.
Profile names allow letters, digits, `.`, `-`, and `_`.

## The `api` Command

The primary command is `cio api <path>`. It makes authenticated HTTP requests to supported Customer.io API endpoints.

```bash
cio api <path> [--method/-X METHOD] [--params '{}'] [--json '{}'] [flags]
```

- Path is a literal API path: `/v1/environments/{environment_id}/campaigns`
- `{placeholder}` values are substituted from `--params`
- Method defaults to GET (or POST if `--json` is provided); override with `-X`
- Query params go in `--params` alongside path params

## Schema Introspection

Before calling any endpoint, inspect its schema:

```bash
cio schema                              # list all resources with endpoint counts
cio schema campaigns                    # list all endpoints for a resource
cio schema campaigns.create             # full detail: path, params, body schema, response schemas, example
cio schema GET /v1/environments/{environment_id}/campaigns
                                        # full schema for a specific HTTP method + path
cio schema /v1/environments/{environment_id}/campaigns
                                        # show all methods for a path
```

Drill down to a specific endpoint (`resource.method`) to get the detailed schema. It includes path/query params, parameter schema details, `request_body_schema`, `response_schemas`, and an example command. The resource-level listing is kept compact on purpose.

## Skills — Domain Knowledge

Skills provide behavioral guidance, multi-step workflows, and gotchas that are NOT discoverable from the API schema alone. **Read the relevant skill before complex operations.**

```bash
cio skills                              # list available skills
cio skills read fly-api                 # Customer.io API guidance — routes to sub-files
cio skills read fly-api/campaigns.md    # campaign workflows, edge wiring, gotchas
cio skills read cli                     # skills unique to the CLI (onboarding, auth/login, integration, go-live)
cio skills read design-studio           # Design Studio email creation workflow
cio skills read design-studio/nodes.md  # node creation, component markup
```

### When to Read Skills vs. Schema

| Need | Use |
|------|-----|
| What endpoints exist for campaigns? | `cio schema campaigns` |
| What params/body does campaigns.create take? | `cio schema campaigns.create` |
| How do I wire campaign actions + edges? | `cio skills read fly-api/campaigns.md` |
| How do I create a Design Studio email? | `cio skills read design-studio/nodes.md` |
| What's the multi-step campaign creation flow? | `cio skills read fly-api/campaigns.md` |

## Global Flags

| Flag | Description |
|------|-------------|
| `--params <json>` | Path + query parameters as JSON object |
| `--json <payload>` | JSON request body (`@filename` / `-` for stdin). With `--arg`/`--argjson` present, it is evaluated as a jq program that builds the body. |
| `--jq <expr>` | Filter output with a jq expression (bundled gojq) |
| `-r, --raw-output` | With `--jq`, print string results unquoted, like `jq -r` (no external jq) |
| `--arg <name=value>` | Bind a string variable for the `--json` jq program (repeatable) |
| `--argjson <name=json>` | Bind a JSON variable for the `--json` jq program (repeatable) |
| `--rawfile <name=path>` | Bind a file's contents as a string variable for `--json` (repeatable) |
| `--slurpfile <name=path>` | Bind a file's JSON contents as a variable for `--json` (repeatable) |
| `-X, --method` | HTTP method override (default: GET, or POST if `--json` is provided) |
| `--dry-run` | Validate and print the request without executing |
| `--read-only` | Request a read-only session; only GET requests are permitted |
| `--scope <value>` | Request additional OAuth scopes during token exchange |
| `--api-url <url>` | Override the API base URL |
| `--token <value>` | Override the service account token |
| `--profile <name>` | Configuration profile to use (overrides `CIO_PROFILE`; default: current profile) |
| `--page <n>` | Page number |
| `--limit <n>` | Page size |
| `--page-all` | Auto-paginate, emit NDJSON (one JSON object per line) |
| `--timeout <dur>` | HTTP request timeout (default: 30s) |

## Exit Codes

| Code | Meaning |
|------|---------|
| 0 | Success |
| 1 | General error |
| 2 | Validation/input error |
| 3 | Authentication error |
| 4 | Authorization error |
| 5 | API error (4xx/5xx) |

## API Examples

```bash
# Read call with --jq to limit output
cio api /v1/environments/{environment_id}/campaigns \
  --params '{"environment_id": "1"}' \
  --jq '.campaigns[] | {id, name, type, state}'

# Write call — always dry-run first, then remove --dry-run to execute
cio api /v1/environments/{environment_id}/campaigns \
  --params '{"environment_id": "1"}' \
  --json '{"campaign": {"name": "Welcome Flow", "type": "none"}}' \
  --dry-run

# Auto-paginate a large collection
cio api /v1/environments/{environment_id}/segments \
  --params '{"environment_id": "1"}' \
  --page-all --jq '{id, name}'

# Build a body with embedded values — --arg binds the string, the --json jq
# program references it; no shell escaping, no external jq
cio api /v1/environments/{environment_id}/campaigns -X POST \
  --params '{"environment_id": "1"}' \
  --arg name="Welcome, with a comma" \
  --json '{campaign:{name:$name,type:"none"}}' --dry-run

# Extract a raw scalar (no surrounding quotes) with -r
ID=$(cio api /v1/environments/{environment_id}/campaigns \
  --params '{"environment_id": "1"}' --jq '.campaigns[0].id' -r)
```

## Filtering Large Responses

```bash
# Basic field selection
cio api /v1/environments/{environment_id}/campaigns \
  --params '{"environment_id": "1"}' \
  --jq '.campaigns[] | {id, name, state}'

# Filter by condition
cio api /v1/environments/{environment_id}/campaigns \
  --params '{"environment_id": "1"}' \
  --jq '.campaigns[] | select(.state == "active") | {id, name}'

# Pagination
cio api /v1/environments/{environment_id}/segments \
  --params '{"environment_id": "1"}' --page 2 --limit 50
cio api /v1/environments/{environment_id}/segments \
  --params '{"environment_id": "1"}' --page-all --jq '{id, name}'
```

## Errors

Errors are structured JSON on stderr:
```json
{"error": true, "code": "VALIDATION_ERROR", "message": "...", "details": {...}}
```

Read the `message` field — it usually tells you what's wrong (missing field, wrong format, etc.).

## Retry Behavior

Automatic retries on HTTP 429 and 5xx with exponential backoff and jitter. Default: 3 retries. Respects `Retry-After` headers.
