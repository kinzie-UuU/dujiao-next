# Security backports, 2026-10-17

Base: `eb0b2903535c10b6cf750941823231d152d3cf97`, same tree as deployed
`e523c43b9debc33ad8e1c1cf8f5da6d3cf682861`. Target branch remains
`codex/upstream-fixes-20260920`; the fork's old `main` is not production.

## Fixed behavior

- Parent and child refunds share one budget, locked parent first. Wallet credits,
  manual refund records and parent accounting commit together. Historical child
  records are reconciled conservatively when calculating the budget.
- Parent cancellations require pending-payment status inside the transaction.
- Product editing preserves unchanged SKU inventory and locked/sold counters.
  Product totals are recomputed from actual active SKU rows, including unlimited
  stock. Explicit zero and minus-one remain supported.
- Verification attempts and one-time consumption are conditional atomic updates;
  code sends reserve their interval and remove undelivered codes. Auth routes
  have IP limits independent of ordinary login lockouts.
- 2FA enablement revokes prior token versions and clears cached auth state; the
  current verified administrator receives a replacement token. CLI password/TOTP
  reset initializes Redis and invalidates auth cache. Redis deletion errors are
  still a degraded path bounded by the existing cache TTL (CLI warns).
- User access and 2FA challenge tokens are bound to the issuing tenant across
  password, registration and social-login adapters. Existing main-site tokens
  remain compatible. Display names reject unsafe/control characters and excess
  length; generated names are sanitized.
- String product/category/content IDs are parsed as unsigned numbers before ORM
  lookup. All uploads receive sandbox/script-src-none CSP and nosniff; SVG still
  downloads, images remain embeddable.
- Generic settings updates cannot write dedicated SMTP, captcha, auth, affiliate,
  email template, Telegram bot and notification keys.
- Upstream image downloads use validated public target IPs, same host/scheme
  redirects, a 10 MiB limit and image MIME sniffing; partial files are removed.
- Procurement and corresponding local order transitions use compare-and-set.
- Failed commissions have bounded queue retries; creation locks/rechecks the
  order and accounts for refunds. Unique creation remains idempotent. Queue
  enqueue failure is logged, not a durable outbox guarantee.
- Reseller public settings do not inherit main-site scripts.

## Compatibility and verification

Payment signatures, amount/underpayment checks, USDT/manual payment logic,
card-secret encryption and fresh payment-admin TOTP remain in place. Existing
payment tests plus new refund/concurrency/state/inventory/auth/upload tests are
required before merge. Run `go test ./...`, `go vet ./...`, Go build, frontend
icon policy, admin/user tests and builds, and release/fullstack embedded tests.

Tests use isolated databases and mocks, not production transactions. SQLite
concurrency coverage is complemented by the explicit parent-first row locking
used on PostgreSQL; this release does not claim a live PostgreSQL stress test.

## Deployment

`operations/security-backports-release` pins and reuses the existing guarded
publisher, including production lock, inventory/plan checks, database backup,
admin overlay preservation and code-only rollback. Deploy the reviewed merged
commit and verify binary revision/hash, health, public catalogue/purchase routes,
admin entry/chunks and upload headers through origin and CDN. No real refunds,
orders, payment callbacks, emails or account resets are release probes.

The read-only preflight found no refunded parent/child orders, so no historical
production data rewrite is needed. Reseller, upstream procurement and affiliate
flows are currently inactive; do not enable them as part of this release.

Local verification: full Go suite, vet, default build, formatting/architecture,
admin 48 and user 56 tests, both frontend builds, fullstack resource tests, icon
policy and 19 existing guarded-publisher tests passed before submission.
