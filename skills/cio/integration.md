# Integration: Add Customer.io SDK to the user's project

Help the user integrate a Customer.io SDK into their application so they can identify users, track events, and optionally enable in-app messaging and push notifications. Adapt to the user's tech stack -- detect it from the codebase or ask.

## Before you start

1. Confirm `cio` is installed and load the CLI reference:
   ```bash
   command -v cio && cio prime
   ```

2. Confirm auth is working:
   ```bash
   cio auth status
   ```

3. Find the workspace (environment) ID:
   ```bash
   cio api /v1/accounts/{account_id}/environments --jq '.environments[] | {id, name}'
   ```

---

## Step 1 -- Detect the user's tech stack

Scan the project for framework markers:

| Marker | SDK |
|--------|-----|
| `Podfile`, `Package.swift`, `.xcodeproj` | **iOS** (Swift) |
| `build.gradle`, `AndroidManifest.xml` | **Android** (Kotlin) |
| `react-native` in package.json | **React Native** |
| `expo` in package.json or `app.json` | **Expo** |
| `pubspec.yaml` with `flutter` | **Flutter** |
| `package.json` with server-side entry (no browser) | **Node.js** |
| `requirements.txt`, `pyproject.toml`, `setup.py` | **Python** |
| `go.mod` | **Go** |
| `index.html`, browser-side JS/TS project | **JavaScript** (browser) |

If multiple apply (e.g. a monorepo with web + API, or mobile + backend), explore the project structure to understand the full stack and recommend the right integration points. For example, a Next.js app with a Python backend might need the browser SDK on the frontend for page views and the Python SDK on the backend for server-side events -- recommend both, but start with whichever is most natural for the user's immediate goal. Don't over-instrument: pick the minimal set of SDKs that covers their use case. One SDK is often enough.

If the project doesn't match any of these, fall back to the **HTTP API** (cURL) approach.

---

## Step 2 -- Get or create a CDP source

Customer.io uses CDP Data Pipelines sources to ingest SDK data. Each SDK type maps to a source `option_id`: `ios`, `android`, `reactnative`, `expo`, `flutter`, `javascript`, `node`, `python`, `go`, `http`.

### Check for existing sources

```bash
cio api /cdp/api/workspaces/{workspace_id}/sources \
  --params '{"workspace_id": "<environment_id>"}' \
  --jq '.sources[] | {id, name, option_id: .slug, enabled}'
```

If a source matching the detected SDK already exists, use it. Skip to Step 3.

### Create a new source

```bash
cio api /cdp/api/workspaces/{workspace_id}/sources \
  --params '{"workspace_id": "<environment_id>"}' \
  --json '{"source": {"name": "<Name> Source", "option_id": "<sdk_type>"}}' \
  --dry-run
```

Remove `--dry-run` after confirming with the user. The response includes a `source.id`.

### Get the write key (API key)

```bash
cio api /cdp/api/workspaces/{workspace_id}/sources/{source_id}/api_keys \
  --params '{"workspace_id": "<environment_id>", "source_id": "<source_id>"}' \
  --jq '.api_keys[] | {id, api_key}'
```

If no API key exists, create one:

```bash
cio api /cdp/api/workspaces/{workspace_id}/sources/{source_id}/api_keys \
  --params '{"workspace_id": "<environment_id>", "source_id": "<source_id>"}' \
  -X POST
```

Save the `api_key` value -- this is the **write key** (also called `cdpApiKey` in mobile SDKs).

---

## Step 3 -- Get credentials for the SDK

The SDK needs different credentials depending on platform:

### All SDKs need

- **Write key** (CDP source API key from Step 2)
- **Region** -- US or EU

Determine the region from the stored config or the account:
```bash
cio auth status --jq '.region'
```

### Mobile SDKs also need

Mobile SDKs (iOS, Android, React Native, Expo, Flutter) additionally require a **site ID** for in-app messaging. Get it from the Journeys workspace:

```bash
cio api /v1/environments/{environment_id}/site_ids \
  --params '{"environment_id": "<environment_id>"}' \
  --jq '.site_ids[0]'
```

---

## Step 4 -- Install the SDK and add initialization code

Install the SDK package and add initialization code. Fetch the README for the SDK you need -- it has the install command and setup instructions. **Run the install command** (`npm install`, `pip install`, `flutter pub add`, etc.) -- don't just add an import and leave the package missing. Replace placeholders with the real write key, region, and site ID from Steps 2-3.

| SDK | README |
|-----|--------|
| JavaScript (browser) | `https://raw.githubusercontent.com/customerio/cdp-analytics-js/main/packages/browser/README.md` |
| Node.js | `https://raw.githubusercontent.com/customerio/cdp-analytics-js/main/packages/node/README.md` |
| Python | `https://raw.githubusercontent.com/customerio/cdp-analytics-python/master/README.md` |
| Go | `https://raw.githubusercontent.com/customerio/cdp-analytics-go/master/README.md` |
| iOS (Swift) | `https://raw.githubusercontent.com/customerio/customerio-ios/main/README.md` |
| Android (Kotlin) | `https://raw.githubusercontent.com/customerio/customerio-android/main/README.md` |
| React Native | `https://raw.githubusercontent.com/customerio/customerio-reactnative/main/README.md` |
| Expo | `https://raw.githubusercontent.com/customerio/customerio-expo-plugin/main/README.md` |
| Flutter | `https://raw.githubusercontent.com/customerio/customerio-flutter/main/README.md` |

For the **HTTP API** (no SDK), use the CDP API docs: https://docs.customer.io/api/cdp/

### Key parameters to supply

- **Write key** (also called `cdpApiKey` in mobile SDKs) -- from Step 2
- **Region** -- US or EU (the SDK README will explain how to configure this)
- **Site ID** (mobile SDKs only, for in-app messaging) -- from Step 3

### Store secrets properly

Never hardcode API keys or write keys in source code. Store them using whatever secret/config mechanism the project already uses:

- **`.env` / `.env.local`** (Next.js, Node, Python, etc.) -- write the key there directly. Use the framework's naming convention (e.g. `NEXT_PUBLIC_` prefix for client-side keys in Next.js).
- **`local.properties`** or Gradle build config (Android) -- write it there.
- **Xcode build settings / `.xcconfig`** (iOS) -- write it there.
- **`--dart-define` or `.env` with `envied`/`flutter_dotenv`** (Flutter) -- write it there.

If the project already has a pattern for secrets (e.g. an existing `.env` file, a secrets manager, a config service), follow that pattern. If it doesn't have one and you can't determine the right approach, tell the user the keys they need to store and where, and let them decide.

---

## Step 5 -- Instrument the app

Don't stop at initialization. Explore the user's codebase and wire in actual calls at the right places. The goal is a working integration, not a README.

### What to wire in

- **`identify`** -- find where users sign up or log in (auth callbacks, session creation, login handlers) and add an `identify` call with the user's ID and profile attributes (email, name, etc.). Use a stable database ID as the userId, not email.
- **`track`** -- find the key user actions in the app (form submissions, purchases, content views, button clicks) and add `track` calls with descriptive event names and relevant properties.
- **`page`/`screen`** -- for web apps, fire `page()` on route changes. For mobile, fire `screen()`. If the app uses a router (Next.js, React Router, etc.), hook into navigation events.

### How to find instrumentation points

Read the app's code to understand the user flows. Look for:
- Auth logic (sign up, login, OAuth callbacks, session hydration)
- Form handlers and API calls that represent user actions
- Route definitions and navigation guards
- E-commerce flows (add to cart, checkout, purchase)
- Content interactions (views, likes, shares)

Pick the highest-value events for the app's domain. A blog needs "Post Viewed"; an e-commerce app needs "Order Completed". Don't over-instrument -- start with 3-5 meaningful events.

### Important notes

- Always call `identify` before `track` so events are attributed to the right person
- Server-side SDKs (Node, Python, Go) require `userId` on every `track` call; client-side SDKs remember it after `identify`
- `created_at` should be a Unix timestamp (seconds) for proper timeline display

---

## Step 6 -- Send transactional emails (optional)

If the user wants to send transactional emails (password resets, order confirmations, receipts, etc.), this is done via a direct HTTP call to the **Track API**. None of the modern SDKs support transactional sending -- it's HTTP-only.

The CLI has built-in commands for this, or the user can call the API directly from their code.

### Using the CLI

```bash
# One-off email (no template needed)
cio send email --environment-id <environment_id> \
  --to user@example.com \
  --from "Acme <noreply@example.com>" \
  --subject "Your order shipped" \
  --body "<h1>Order #123 is on its way</h1>"

# Template-based transactional email
cio transactional send email --environment-id <environment_id> \
  --transactional-message-id 1 \
  --to user@example.com \
  --message-data '{"name":"Alice","order_id":"123"}'

# List available transactional templates
cio transactional list --environment-id <environment_id>
```

### From code (HTTP API)

For production backend code, **issue a workspace-scoped App API key** — do not embed the `sa_live_` SA token. SA tokens are account-level (full access, all workspaces); App API keys are scoped to a single workspace and are the right fit for backend integrations.

Create one via the CLI (one-time setup):

```bash
cio api /v1/environments/{environment_id}/ext_api_keys -X POST \
  --json '{"ext_api_key":{"name":"backend-prod"}}'
```

The response includes the full Bearer value on creation — capture it and copy into the backend's env var (e.g. `CIO_APP_API_KEY`). Subsequent `GET .../ext_api_keys` calls return only a hint (last few characters), not the full key. Lost keys can't be recovered — issue a new one.

Endpoints — one per channel, all share a common base shape:

| Channel | Path |
|---------|------|
| Email | `POST /v1/send/email` |
| Push | `POST /v1/send/push` |
| SMS | `POST /v1/send/sms` |
| In-app | `POST /v1/send/in_app` |
| Inbox | `POST /v1/send/inbox_message` |

App API base URL varies by region:

| Region | App API base URL |
|--------|------------------|
| US | `https://api.customer.io` |
| EU | `https://api-eu.customer.io` |

The HTTP shape is the API contract — produce the equivalent in whatever language the user is using.

```http
POST https://api.customer.io/v1/send/email
Authorization: Bearer <APP_API_KEY>
Content-Type: application/json

{
  "transactional_message_id": "order_confirmation",
  "auto_create": true,
  "identifiers": { "id": "user-123" },
  "to": "user@example.com",
  "message_data": { "name": "Alice", "order_id": "123" }
}
```

For one-off emails without a template, include the content inline (no `transactional_message_id` needed):

```http
POST https://api.customer.io/v1/send/email
Authorization: Bearer <APP_API_KEY>
Content-Type: application/json

{
  "to": "user@example.com",
  "identifiers": { "email": "user@example.com" },
  "from": "Acme <noreply@example.com>",
  "subject": "Your order shipped",
  "body": "<h1>Order #123 is on its way</h1>"
}
```

The `X-Workspace-Id` header is **not** needed when using an App API key — the key is already workspace-scoped. (The header is only needed for SA-token-based calls, which the CLI itself handles internally.)

### The `auto_create` paradigm — simplest pattern for backend sends

Pick a stable string identifier (`"order_confirmation"`, `"password_reset"`, `"shipping_update"`) and pass it as `transactional_message_id` along with `auto_create: true`. The first call creates a transactional message in the workspace with that name and the channel matching the endpoint you hit (`/v1/send/email` → email-typed message, `/v1/send/push` → push, etc.). Subsequent calls find and reuse the existing message. Keep `auto_create: true` on every send — it's idempotent.

Name constraints: must be a non-numeric string, non-empty, ≤ 191 unicode characters. A name that parses as a number (e.g. `"42"`) is rejected; `auto_create` is text-only.

**When NOT to use `auto_create`:**

- **In-app and inbox messages** — auto-created templates have no `body_json`, so deliveries queue successfully but render empty. Create the message explicitly via the management API or UI first, then configure the template.
- **When the template is authored ahead of time** (e.g. the email body is built in Design Studio or the UI). Create the message explicitly and call with the resulting numeric ID or string name; omit `auto_create`.

### Channel notes

- **Email:** can send *with* a `transactional_message_id` (templated) or *without* (inline content via `to`, `from`, `subject`, `body`, `body_plain`).
- **Push, SMS, in-app, inbox:** always require a `transactional_message_id`.
- The full per-channel field list (push `custom_payload`, SMS `tracked`, etc.) is in the API reference: https://customer.io/docs/api/app/#tag/Transactional

Docs: https://docs.customer.io/journeys/transactional-email/ · https://customer.io/docs/api/app/#tag/Transactional

### Important notes

- For backend code, use a workspace-scoped App API key (Bearer auth against `api.customer.io`) — not the `sa_live_` SA token. The CLI's own `cio send` and `cio transactional send` commands use the SA token internally, which is fine for testing, but production code should use a per-workspace App API key.
- Do NOT retry failed sends automatically -- retrying a POST risks duplicate deliveries
- For EU regions, use `https://api-eu.customer.io`

---

## Step 7 -- Verify the integration

### Check source status

```bash
cio api /cdp/api/workspaces/{workspace_id}/sources/{source_id}/status \
  --params '{"workspace_id": "<environment_id>", "source_id": "<source_id>"}'
```

### Check for the identified user in the workspace

After sending an `identify` call from the app, verify the profile was created:

```bash
cio api /v1/environments/{environment_id}/customers \
  --params '{"environment_id": "<environment_id>", "filter": "email = \"user@example.com\""}' \
  --jq '.customers[] | {id, email}'
```

### Check recent events on a source

```bash
cio api /cdp/api/workspaces/{workspace_id}/sources/{source_id}/events \
  --params '{"workspace_id": "<environment_id>", "source_id": "<source_id>", "limit": "5"}' \
  --jq '.events[] | {type, event: .properties.event, userId: .properties.userId, timestamp}'
```

---

## Troubleshooting

### Events not appearing

1. Verify write key is correct (not the App API key or site ID)
2. Check region matches -- US workspace needs US endpoint, EU needs EU
3. Ensure `identify()` is called before `track()`
4. For server-side SDKs, check that the SDK is flushing (call `close()` or `flush()`)

### Auth errors (401)

- Write key goes in Basic auth header as username, password is empty
- Mobile SDKs use `cdpApiKey` parameter, not Basic auth
- Don't confuse the CDP write key with the Journeys Track API key or App API key

### In-app messages not showing

- `identify()` must be called first
- SDK must be initialized with `siteId` (mobile) or the In-App Plugin (web)
- User must be in the foreground with the app/page visible
- Check in-app messaging is provisioned: verify via the Customer.io UI under Settings > In-App

### Push notifications not arriving

- **iOS**: test on a real device (not simulator), add Notification Service Extension
- **Android**: verify `google-services.json` placement, create notification channels, request `POST_NOTIFICATIONS` on Android 13+
- **React Native**: run `pod install` for iOS after package changes
- **Expo**: use Development Build, not Expo Go; run `npx expo prebuild`
