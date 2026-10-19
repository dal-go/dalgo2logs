---
format: https://specscore.md/idea-specification
status: Draft
---

# Idea: My DataTug and My Sneat data transparency

**Status:** Draft
**Date:** 2026-10-19
**Owner:** alex
**Promotes To:** —
**Supersedes:** —
**Related Ideas:** —

## Problem Statement

How might users inspect their own product data through curated, permissioned DataTug collections without exposing internal storage or crossing identity boundaries?

## Context

Users should be able to see the DataTug data and activity that affect their
workspace, even before adding an external connection. Sneat data can offer a
similar experience only after explicit account linking; its users, spaces,
and organizations are separate authorization domains. This is a design-only
track and does not delay the direct log investigation.

## Recommended Direction

Expose curated logical, read-only collections through server-owned adapters:
`my_saved_queries`, `my_query_runs`, `my_investigations`, `my_checks`,
`my_activity`, and `workspace_usage`. Each declares its scope, retention,
redacted fields, and cost provenance. The product value is immediate
onboarding, transparent usage and billing, and self-service troubleshooting.
Separate personal rows from workspace rows in the UI and in authorization.
Query text may be sensitive and should default to metadata or redacted form.

Offer My Sneat as a separately consented connection with narrow read scopes
for a user's reminders, lists, budgets or trackers, and appropriate activity
records. A Sneat account-link record binds two explicit principals, selected
spaces, requested scopes, expiry, and revocation. It never equates a DataTug
workspace with a Sneat space by name or email. Start with one low-risk
collection and an obvious disconnect control.

The server resolves identity and scope on every request, applies row and field
permissions before query execution and export, and logs access without raw
payloads. Shared investigations carry a separate evidence-sharing policy;
they must not accidentally grant access to a source. A recipient sees only
evidence they are authorized to re-fetch or an explicitly approved redacted
copy. AI use treats log and data content as untrusted input.

### Product journeys and onboarding

1. **My DataTug, no external connection.** A signed-in user opens a built-in
   source catalogue. Separate **Personal** and **Workspace** sections explain
   which principal grants each collection and how current the data is. The
   user opens a failed query run, sees its timing, result status and a redacted
   query fingerprint, follows its saved query and related check, then shares
   an investigation link. A recipient must pass the same source authorization
   check before the link resolves its evidence.
2. **My Sneat, explicit connection.** The user selects **Connect Sneat**, signs
   in to Sneat independently, sees requested read scopes and selects spaces.
   DataTug shows the linked Sneat identity, selected spaces, expiry and a
   **Disconnect** control. A user can then inspect their own reminders and
   related activity in the same grid/investigation UI. Revocation makes live
   rows unavailable immediately, including from saved investigation links.

The first screen should offer a few useful read-only example queries, scope
labels, freshness and retention notes, and a clear empty state. It must never
suggest that joining a DataTug workspace grants Sneat space access.

### Curated logical collections

These are proposed stable views, not physical table or Firestore names. Each
source owner must approve a field-level schema and retention before rollout.
Opaque IDs shown to clients must be scoped and must not serve as authorization.

| Source | Scope | Initial fields and deliberate omissions |
|---|---|---|
| `my_saved_queries` | Personal or selected workspace | Title, owner, revision, last run, safe tags; query text hidden by default and revealed only when separately authorized. |
| `my_query_runs` | Actor or selected workspace | Time, status, duration, rows, source label, error category and safe fingerprint; no credentials, raw SQL parameters or unrestricted error stack. |
| `my_investigations` | Creator or explicitly shared workspace | Title, evidence reference list, share scope, updated time; resolve each reference through its source permission on every open. |
| `my_checks` | Personal or selected workspace | Definition name, schedule, latest outcome and safe failure category; redact connection details. |
| `my_activity` | Actor or authorized workspace auditor | Curated action, actor class, time, object type and safe object reference; no raw operational log payload. |
| `workspace_usage` | Workspace billing role | Meter, unit, period, source, freshness and provenance. Keep actual billed amounts, estimates and raw usage as distinct types; never infer an invoice from usage. |
| `sneat_reminders`, `sneat_lists` | Linked principal plus selected Sneat space | User-owned title/status/due time and scoped IDs; omit members' private fields and attachments until separately reviewed. |
| `sneat_budget_entries`, `sneat_activity` | Separately consented sensitive scope | Narrow, redacted financial/activity summaries only after Sneat owner and privacy review; exclude them from the initial pilot. |

Every collection has a server-owned projection, bounded page size, stable sort,
source-specific retention and allowed predicates. Unsupported joins, arbitrary
query text, export, or search must be rejected explicitly. A workspace user
may see different fields and rows from a workspace auditor; these are
different server authorization policies, not UI filters.

### Adapter and API proposal

Place a private authorization gateway between the existing DataTug
query/investigation UI and curated DALGO-backed read adapters. The gateway
enumerates only source descriptors authorized for the current principal and
returns capabilities, field labels, scope and freshness. On each query it
derives the DataTug subject and workspace membership from server credentials,
then injects row and field policy before the adapter executes. Client-supplied
workspace, owner or record IDs are selectors subject to that policy, never
trusted scope. Use the existing grid and evidence-reference behavior where
possible. This design does not add a public OVDB source type or extend its
protocol; any gateway endpoint shape is subject to product/API review before
implementation.

The Sneat adapter receives a short-lived, server-stored link grant bound to
the DataTug principal, the independent Sneat principal, selected spaces,
specific read scopes, expiry and revocation state. Sneat remains the authority
for space membership on every read. No browser-held long-lived Sneat token,
email matching, automatic organization mapping, or client-side tenant filter
is acceptable. Revoking in either product invalidates the link and cached
authorization; DataTug records a safe audit event without storing Sneat raw
payloads in application logs.

### Retention, export and cost semantics

Start with live, permission-checked references in investigations. Do not copy
query text, Sneat financial rows or raw logs into a case by default. If an
approved redacted evidence copy is later offered, it needs an explicit owner,
recipient list, expiry, deletion path and audit trail distinct from the source
retention policy. Exports must be server generated after the same row/field
checks; deletion and account disconnect must remove cached source data and
invalidate links, subject to stated legal or billing retention. Show a clear
``unavailable or no longer authorized`` state for stale references rather
than displaying a prior cached value.

Usage responses should carry `kind` (`usage`, `estimate`, or `actual`), unit,
period, source-of-record, calculation version, update time and currency only
when money is involved. An `estimate` needs an uncertainty or assumptions
label. Only a billing source-of-record may populate `actual`; present
estimated AI cost separately from invoices and allow users to inspect the
metering inputs without exposing other tenants' data.

### Threat model and verification gates

| Threat | Control | Required test before release |
|---|---|---|
| IDOR or cross-tenant leakage through guessed IDs, filters or pagination | Server derives principal and scope for every page and evidence re-fetch | Colliding IDs across workspaces and Sneat spaces; changed membership mid-pagination; forged selectors. |
| Secret/PII exposure through query text, errors, activity or export | Allowlisted projections, default redaction, export policy, safe diagnostics | Seed credentials and PII in each upstream surface; assert absent in API, UI, logs and exports. |
| Sneat token reuse or stale consent | Narrow short-lived server grant, independent identity, expiry and revocation | Revoke in DataTug and Sneat; verify immediate read and shared-link denial. |
| Prompt injection in log or record content | Treat evidence as data, never instructions; require citations to authorized rows | Inject instruction-like payloads and verify optional AI cannot invoke tools or reveal hidden data. |
| Shared investigation widening source access | Reauthorize each live reference for each viewer; explicit redacted-copy policy | Share across roles and tenants; assert denied rows and fields stay hidden. |
| Misstated cost or retention | Type actuals/estimates/usage separately; provenance and deletion policy | Reconcile billed sample periods, revisions, export and deletion behavior. |

### Staged rollout

1. **Internal opt-in:** `my_saved_queries` and redacted `my_query_runs`,
   personal scope only. Gate on source-owner schema approval, IDOR/redaction
   tests, bounded query limits and audit review.
2. **Workspace pilot:** Add checks, investigations, activity and usage with
   role-specific projections. Gate on cross-tenant sharing, billing provenance,
   export/deletion and support incident drills.
3. **Sneat pilot:** Separate consent for one low-risk reminder/list collection
   and selected spaces. Gate on independent identity and space membership,
   scope revocation, token handling and Sneat owner review.
4. **Broader release:** Expand sensitive Sneat financial/activity fields only
   after explicit privacy/security sign-off. Measure onboarding completion,
   support self-service and authorization failures before wider exposure.

## Alternatives Considered

- Direct Firestore/production database exposure: unstable schema and broad
  access could leak secrets or cross tenant boundaries.
- Client-side filtering of one broad endpoint: allows IDOR, guessed IDs and
  unauthorized export; scope must be enforced before results leave the server.
- Automatic Sneat access by matching email: identities and memberships can
  differ, and consent cannot be inferred.

## MVP Scope

Design only: collections, authorization, consent, threat model, staged rollout and tests.

## Not Doing (and Why)

- Production integration — outside dalgo2logs MVP

## Key Assumptions to Validate

| Tier | Assumption | How to validate |
|------|------------|-----------------|
| Must-be-true | Server maps every principal to allowed collections, rows and fields | Cross-tenant and IDOR tests using colliding IDs and revoked memberships |
| Must-be-true | Sneat link is explicit, scoped and revocable | Consent/revocation E2E with independent identities and spaces |
| Should-be-true | Usage can distinguish metered facts from estimates | Reconcile sample invoices and label non-billable estimates |
| Might-be-true | Curated activity helps support triage | User study on a redacted prototype |


## SpecScore Integration

- **New Features this would create:** My DataTug source, consented My Sneat source, scoped sharing
- **Existing Features affected:** none
- **Dependencies:** none

## Open Questions

- Define collection schemas, pagination, retention and deletion/export rules
  with the owning product teams before a production adapter is built.
- Which costs are authoritative billing records, and which are estimates?
- Which existing audit events are safe for user access after PII and secret
  review? Threat-model credential leakage, prompt injection, cross-tenant
  access, IDOR and shared-investigation disclosure before rollout.
- Roll out first as internal opt-in, then workspace admin opt-in, then
  selected users. Gate each stage on authorization, redaction, revocation,
  export and audit tests. No production connector is implemented here.
