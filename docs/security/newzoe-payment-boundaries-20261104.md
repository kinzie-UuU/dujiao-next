# Payment and procurement boundary audit (2026-11-04)

Scope: production base `5facec9b598fa027670014c0a80fffbf3cc434ec`.
Read-only production evidence: `dujiaonext-api`, image
`dujiao-next:newzoe-5facec9b598f-10ffa83cfb96`, running executable SHA-256
`10ffa83cfb968bd278838957bd1160f770fc6bd55a0e58f476a766e614a2c0a2`.
No production mutations, deployment, or paid transactions performed by this audit.

## Reproduced and repaired

- A stale procurement parent-status synchronization could overwrite a concurrent
  refund. Status compare-and-set now preserves the newer state; fully refunded
  parents are terminal. Regression injects the refund at the actual write boundary.
- A late generic upstream partial-refund callback could reopen a fully refunded
  procurement. Fully refunded procurement state is now terminal.
- A downstream callback response could echo fulfillment secrets into error logs.
  Errors now expose only HTTP status. Both success and failure order-reference
  logs omit callback URLs (which can carry query credentials).
- The generic downstream purchasing API accepted replayed signed mutations, and
  a failed reference reservation still proceeded to wallet payment. Mutations now
  require an atomic Redis SET NX claim for the full timestamp acceptance window;
  missing/unavailable Redis returns 503. Repeated exact requests return 409.
  A business retry re-signs after the timestamp changes and retains the same
  downstream order number. GET/HEAD/OPTIONS and the read-only ping remain retryable.
  HMAC verification accepts only canonical lowercase hex, so signature spelling
  cannot bypass the replay key.
- Business order-number reservation is serialized separately from replay checks:
  PostgreSQL/MySQL lock the credential row; SQLite obtains its write reservation
  with a no-op credential UPDATE before reading/inserting the reference. Any
  locking/query/write failure stops before payment. Duplicate contenders cancel
  their newly-created unpaid order and return 409. Subsequent business retries
  return the stored order without invoking payment again.

Production has zero downstream API credentials and zero downstream order
references. These downstream changes are preventive; they do **not** describe a
replay vulnerability in the active NewZoe outbound adapter. NewZoe continues to
use its existing signed requests and stable local-order idempotency key.

## Compatibility and release notes

- No schema migration or Go dependency changes. Merge onto the exact intended
  production base, run the verification gates, build with the pinned production
  toolchain, and deploy using the existing guarded release procedure.
- Keep the existing Redis connection configured. Redis failure now deliberately
  blocks signed downstream writes; NewZoe outbound procurement does not use this
  new middleware or key namespace.
- A crash after reference reservation but before payment may leave an unpaid
  pending reference. Retrying returns that order and does not automatically debit
  it. This favors no duplicate charge over automatic recovery; downstream is
  currently unused. Reference reservation and payment are not one transaction.
- Redis replay records are short-lived. Do not flush Redis or switch to an empty
  Redis instance while enabling downstream traffic inside the timestamp window.
  Durable nonempty downstream order numbers remain the business deduplication
  boundary across restarts.
- Preserve rollback binary and database backups. Post-release checks should
  confirm process health, existing order reads, Redis connectivity, and public
  catalog accessibility. They do not substitute for a real paid purchase.

## Verification evidence

Fresh scanner: `go run golang.org/x/vuln/cmd/govulncheck@latest ./...`, Go 1.26.10,
completed `2026-11-04T01:47:37Z`: **No vulnerabilities found**. This is known
vulnerability analysis of the current source dependency graph, not proof of no
application vulnerabilities.

Affected procurement/downstream/API/middleware suites pass under `go test -race`.
SQLite reservation tests pass ten repeated race-enabled runs (16 concurrent
writers each). HTTP tests cover reference lookup/write failure, duplicate
reservation, existing-order retries, Redis failure, invalid signatures, and
concurrent signature replays. Parent refund, terminal callback, and response-body
regressions reproduced against the pre-fix implementation before passing fixes.
`go vet` for all affected modules and HTTP server passes.

Final `go test ./...` passed. The first run caught architecture file-count limits
for the new test files; tests were consolidated into existing suite files, and
both architecture checks and the full suite then passed without raising limits.
Additional callback URL construction/transport failure regressions pass under
race detection. Unused dujiaopay/wechatpay gateway non-2xx errors also omit raw
response bodies, with their existing gateway suites passing.
