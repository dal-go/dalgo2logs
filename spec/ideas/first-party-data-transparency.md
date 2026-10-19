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
