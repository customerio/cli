You have access to `cio`, an agent-first CLI for Customer.io. Most commands return structured JSON to stdout, and errors return structured JSON to stderr. The `cio prime` command itself outputs Markdown for prompt/context injection.

## Key Rules

1. Unless you are intentionally using `cio prime`, assume command output is JSON. Parse it, don't regex it.
2. Resource IDs are integers (but passed as strings in paths and params).
3. ALWAYS use `--jq` on read calls to limit output and save tokens.
4. ALWAYS use `--dry-run` before any mutating call to preview what will be sent.
5. Read the relevant skill (`cio skills read`) BEFORE making complex API calls — the API has non-obvious required fields, multi-step workflows, and silent failures.

## Authentication

Auth uses service account tokens exchanged for JWTs. Check status with:

```
cio auth status
```

If auth fails, ask the user to run `cio auth login` and paste their `sa_live_` token.

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
| `--json <payload>` | JSON request body or `@filename` to read from file |
| `--jq <expr>` | Filter output with jq expressions (via gojq) |
| `-X, --method` | HTTP method override (default: GET, or POST if `--json` is provided) |
| `--dry-run` | Validate and print the request without executing |
| `--read-only` | Request a read-only session; only GET requests are permitted |
| `--scope <value>` | Request additional OAuth scopes during token exchange |
| `--api-url <url>` | Override the API base URL |
| `--token <value>` | Override the service account token |
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
