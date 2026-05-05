# Billing: Plans, pricing, and go-live costs

Use this when the user asks about pricing, plans, current limits, billing
status, invoices, receipts, payment methods, credits, or whether they need to
pay before launching.

Keep the answer short, account-specific, and grounded in current sources. Do not
paste raw billing JSON unless the user asks for it. Do not quote prices,
minimums, message rates, or allowances from memory.

## Workflow

1. Check the account's current billing state.
2. If the account is on Builder, check the Builders page. Otherwise check the
   current pricing page for the relevant plan or product path.
3. Explain what the user's current plan allows for their goal.
4. Point them to the next product-supported step in Account Settings, the
   pricing page, or sales.

Do not assume the user is on Builder, Essentials, Premium, or any other plan.
Do not infer a payment path from route names alone.
Do not say Builder is only for testing. Builder has a test mode and a
pay-as-you-send go-live path; use the current Builders page for the exact
minimum, rate, and unlock steps.

## Check Billing State

Get the account ID if you need it:

```bash
cio auth status --jq '.account_id'
```

Then inspect billing. Match `subscription.plan_id` to `plans[].id`; do not
treat every item in `plans` as the current plan.

```bash
cio api /v1/accounts/{account_id}/billing/info \
  --jq '. as $b | {
    subscription: $b.subscription,
    billing_info: ($b.billing_info | {
      active,
      active_subscription,
      basic_trial,
      premium_trial,
      billing_estimates,
      estimated_invoice_cost,
      overdue,
      has_failed_overage_charge,
      can_retry_payment
    }),
    current_plan: ([$b.plans[]? | select(.id == ($b.subscription.plan_id // null) or .current == true)] | first)
  }'
```

If the plan is still unclear, list account plan options:

```bash
cio api /v1/accounts/{account_id}/billing/plans
```

Translate the result into plain language:

- current plan or billing model
- active, trial, or inactive subscription state
- limits, allowances, or coverage shown for the account
- current usage or overage estimate, if relevant
- the next step shown by the product or current pricing page

Do not call `tiers` or `tiers_count` usage, limits, or overages. Tiers are plan
pricing rows unless an endpoint explicitly labels them as current account usage.
Do not turn boolean flags like `has_overage` into a bill estimate by themselves;
use `billing_estimates` or an explicit overage endpoint for amounts.

## Check Current Pricing

Use WebFetch, a browser, or a search/docs tool to read these pages before
quoting exact pricing, limits, minimum purchases, or rates:

- Builder / pay-as-you-send: https://customer.io/builders
- Paid plans: https://customer.io/pricing
- Billing mechanics and overages: https://docs.customer.io/accounts-and-workspaces/how-we-bill/

Do not scrape marketing pages with `curl`, `grep`, or partial HTML and then
guess. If you cannot read the page, link the source and avoid exact prices.

## Going Live

Say what the current account state means for the launch the user described:

- If they can continue testing, say so.
- If `current_plan.name`, `display_name`, or `plan_type` is Builder, check the
  Builders page first. Do not say Builder is testing-only or that Builder must
  upgrade to a paid plan to send externally; use the Builders page for the
  current pay-as-you-send/external-send unlock path. If you cannot read the
  page, link it and say you could not verify exact current pricing.
- If the product directs them to upgrade, say which upgrade path the current
  pricing page shows and link to that flow.
- If they need a payment method, billing access, or sales contact, say that.
- If they ask for exact cost, quote the current pricing source you checked and
  link to it.

Do not issue `POST`, `PUT`, `PATCH`, or `DELETE` requests to billing endpoints.
If the user wants to change billing, direct them to the Customer.io billing UI,
pricing page, or sales path unless a product owner explicitly instructs
otherwise.
