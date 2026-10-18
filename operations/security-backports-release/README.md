# 2026-10-17 security backports release

Run `release.py` with `--controls` pointing to the `operations` directory from
`kinzie-UuU/kawang-AIGC` commit `2d1615e072744ad69ed65f652b97fce36a48c50d`.
The two reused controller files are SHA256 pinned; no unreviewed helper is loaded.

Production baseline is the reviewed e523c43 payment-security image and binary.
The controller preserves live admin overlays, invoice entry and storefront;
creates a consistent SQLite backup; checks production inventory/plan hashes;
uses the common production lock; and rolls back code only on failure. It never
restores the business database. Runtime image derives from the exact existing
image, retaining runtime layers. API plus admin assets are published together.

Sequence: `inventory`, `plan`, then `deploy --production-write` using the exact
returned state and plan hashes, final merged revision, Linux amd64 fullstack
binary SHA256 and admin dist. Review the plan and live-overlay classification
before deployment. User has authorized this release; hashes are drift gates.

Build both frontends and Go release/fullstack from the reviewed merged commit.
Record local checks, hosted CI, deployed revision/hash and public readback
separately. Never use real refunds or payments as release probes.
