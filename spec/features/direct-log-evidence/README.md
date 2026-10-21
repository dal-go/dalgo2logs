---
format: https://specscore.md/feature-specification
status: Implementing
---

# Feature: Direct log evidence sources

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/dal-go/dalgo2logs/spec/features/direct-log-evidence?op=explore) | [Edit](https://specscore.studio/app/github.com/dal-go/dalgo2logs/spec/features/direct-log-evidence?op=edit) | [Ask question](https://specscore.studio/app/github.com/dal-go/dalgo2logs/spec/features/direct-log-evidence?op=ask) | [Request change](https://specscore.studio/app/github.com/dal-go/dalgo2logs/spec/features/direct-log-evidence?op=request-change) |
**Status:** Implementing
**Source Ideas:** —

## Summary

Read file and Google Cloud Logging entries through DALGO and unchanged OpenVaultDB queries for a DataTug investigation.

## Problem

An order record says what remains unset but not why. A support engineer needs
related operational evidence with stable references, while OpenVaultDB remains
a general database interface. Copying logs into another database introduces
storage, staleness and permissions that this investigation does not need.

## Behavior

The caller configures named files under an allowed root, or one Google Cloud
Logging project. Each direct source exposes a read-only DALGO collection.
The file adapter parses explicit JSONL or timestamped text and scans the
current bounded file for each query. The Cloud adapter sends a bounded
entries.list query for an inclusive time range. Both preserve raw evidence
and source references, and reject unsupported filters and every mutation.

A trusted JSONL `Source.TimestampField` can select one exact top-level ASCII
identifier (1–128 characters, `[A-Za-z_][A-Za-z0-9_]{0,127}`) as the observed
timestamp. Empty configuration preserves legacy `timestamp`/`time` selection.
Explicit mapping is authoritative: missing/null is `missing_timestamp`;
present empty, non-string or invalid RFC3339Nano is `invalid_timestamp`, with
no alternate-field fallback. Valid values normalize to UTC. This configuration
is not request input and changes neither original fields/raw evidence nor its
hash/range references, existing schema or OVDB wire fields. Text sources reject
a nonempty mapping.

A trusted Go process mounts a DALGO driver through OVDB's programmatic
read-only OpenExternal method. OVDB uses existing discovery, schema, query
and results endpoints; it has no log parser or log-specific wire field.
DataTug maps ordinary fields to an AG Grid timeline and a known order ID to
the related record. The Northwind fixture pairs real order 11039 with
clearly synthetic log entries, and never claims historical causation.

OVDB discovery returns `engine: "external"` for this mount. The `engine`
property is an existing open string in the public descriptor, so this value
is visible to clients without adding a route, response field, enum or log
type. An ordinary OVDB client discovers and queries the collection using its
existing capabilities and query endpoints. File manifests still reject
`external`; the mount is programmatic and read-only.

## Delivery status (updated 2026-10-20)

| Slice | Receipt | State |
| --- | --- | --- |
| JSONL and multiline text, bounded read-only DALGO adapters, delegated Cloud Logging reader | [`dalgo2logs` PR #12](https://github.com/dal-go/dalgo2logs/pull/12) | Merged; direct reads preserve raw evidence and references without a log database or snapshot |
| Generic programmatic OVDB mount | [`openvaultdb-go` PR #88](https://github.com/openvaultdb/openvaultdb-go/pull/88) | Merged; no new route, field or log-specific protocol semantics |
| DataTug evidence UI | [`datatug-apps` PRs #398 and #415](https://github.com/datatug/datatug-apps/pull/415) | Merged; local evidence rows use the real file adapter and unchanged OVDB endpoints; hosted connection/query UI remains behind the verified-UID gate |
| Reproducible synthetic Northwind evidence and hosted pilot guide | [`datatug-demo-project` PRs #55, #62, #63 and #64](https://github.com/datatug/datatug-demo-project/pull/64) | Merged; local fixture includes six related synthetic events, raw evidence, and real AG Grid captures; the guide records the separate production Google pilot |
| Hosted personal Cloud Logging | [`datatug-cloud` PR #25](https://github.com/datatug/datatug-cloud/pull/25); [DataTug feature PR #184](https://github.com/datatug/datatug/pull/184) | Merged; opt-in hosted service is configured in existing project `sneat-eur3-1`. The owner consented to `openid email logging.read` with offline access and queried the dedicated synthetic view. The broader `_Default/_AllLogs` view probe returned 403. Removing and reconnecting the synthetic view succeeded without new Google consent; a clean-tab query at 15:06 UTC returned six rows in the actual AG Grid. |

The hosted pilot uses the existing `sneat-eur3-1` project with a dedicated OAuth
client, KMS key, and `roles/logging.viewAccessor` binding on the synthetic
view; a separate disposable project is not required for this pilot. The
production OAuth consent screen remains unverified, with verification and its
project-wide usage cap to resolve before broader rollout. The later clean-tab
query used the retained grant and view, but Google grant expiry and refresh
behavior are unverified, and Google-wide disconnect/revocation has not been
performed. See the [hosted pilot receipt](https://github.com/datatug/datatug-demo-project/blob/main/docs/logs/google-cloud-pilot.md)
for the bounded query, remove/reconnect, access-denial, and re-entry evidence.

The automated hosted browser tests use emulators and synthetic API responses;
they remain separate from the live consent and production Google query above.
The local scenario is reproducible without AI. No log entries are ingested into
a database or snapshot. Hosted log-to-record navigation, persistent Detective
cases, and general field mapping for existing database log tables remain open.

## Acceptance Criteria

- A fresh local setup reads both file formats directly and a normal OVDB
  client discovers and queries the collection through existing routes.
- Every file query enforces a trusted root, entry/file/result caps and
  cancellation. File query planning accepts at most 16 comparisons, 16 nested
  AND groups and 64 planner visits. Each planner invocation counts, including
  nil children and AND groups (including empty groups); exceeding a limit
  returns `dal.ErrNotSupported` before file access. A pre-canceled query
  returns its context error before planning. Cloud queries enforce project,
  time window, result and duration bounds. No log entry is ingested into a
  database.
- Unsupported query shapes and writes fail explicitly. Missing and malformed
  timestamps remain visible without fabricated values.
- DataTug opens order 11039, follows its explicit order_id mapping to
  chronological evidence, opens raw/multiline details, and returns to the
  order. The documented conclusion cites both record and synthetic log facts.
- Tests cover file, Cloud filter construction, ordinary OVDB client access,
  failure modes and UI states. A standalone HTML walkthrough explains the
  same implemented path.

## Open Questions

- Before broader rollout, resolve Google OAuth verification and project-cap
  requirements, establish expiry/refresh and disconnect/revocation behavior,
  and confirm billing/quota bounds. The live pilot verifies a bounded read of
  its synthetic view, not Google-wide revocation or refresh-token behavior.
- Hosted log-to-record navigation remains incomplete. The local file scenario
  pairs real order 11039 with synthetic logs and does not claim historical
  causation.
- Detective case persistence is not established. For now the shareable route,
  fixture revision, saved query and evidence references reconstruct the
  investigation; a durable case is separate follow-up work.
- General field mapping for existing database log tables remains open.

---
*This document follows the https://specscore.md/feature-specification*
