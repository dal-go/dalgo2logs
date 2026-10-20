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

## Delivery status (2026-10-19)

| Slice | Receipt | State |
| --- | --- | --- |
| JSONL and multiline text, bounded read-only DALGO adapters, delegated Cloud Logging reader | [`dalgo2logs` main](https://github.com/dal-go/dalgo2logs/tree/62c8d1efd058915c82fc726cd8ce201aa602faeb) | Merged; direct reads, no log database or snapshot |
| Generic programmatic OVDB mount | [`openvaultdb-go` PR #88](https://github.com/openvaultdb/openvaultdb-go/pull/88) | Merged; no new route, field or log-specific protocol semantics |
| Local AG Grid evidence and record return | [`datatug-apps` PR #398](https://github.com/datatug/datatug-apps/pull/398) | Merged; two local Playwright tests passed against the real file adapter and unchanged OVDB endpoints |
| Reproducible synthetic Northwind evidence and HTML | [`datatug-demo-project` PR #55](https://github.com/datatug/datatug-demo-project/pull/55) | Merged; six related events, raw evidence and real AG Grid screenshots |
| Hosted personal Cloud Logging | [`datatug-cloud` PR #9](https://github.com/datatug/datatug-cloud/pull/9) | Host code merged but disabled; real consent, narrow-view IAM, Cloud Run cleanup liveness and read/revoke pilot remain open |

The local scenario is reproducible without AI. The hosted browser test uses
emulators and synthetic API responses; it does not establish access to a real
user's Google Cloud logs. Persistent Detective cases and general field mapping
for existing database log tables are not delivered by this slice.

## Acceptance Criteria

- A fresh local setup reads both file formats directly and a normal OVDB
  client discovers and queries the collection through existing routes.
- Every file query enforces a trusted root, entry/file/result caps and
  cancellation. File query planning accepts at most 16 comparisons, 16 nested
  AND groups and 64 visited condition nodes (comparison and group nodes count,
  including empty groups); exceeding a limit returns `dal.ErrNotSupported`
  before file access. A pre-canceled query returns its context error before
  planning. Cloud queries enforce project, time window, result and duration
  bounds. No log entry is ingested into a database.
- Unsupported query shapes and writes fail explicitly. Missing and malformed
  timestamps remain visible without fabricated values.
- DataTug opens order 11039, follows its explicit order_id mapping to
  chronological evidence, opens raw/multiline details, and returns to the
  order. The documented conclusion cites both record and synthetic log facts.
- Tests cover file, Cloud filter construction, ordinary OVDB client access,
  failure modes and UI states. A standalone HTML walkthrough explains the
  same implemented path.

## Open Questions

- The hosted per-user authorization design and disabled implementation are
  recorded in the [DataTug feature](https://github.com/datatug/datatug/blob/main/spec/features/hosted-google-cloud-log-evidence/README.md).
  A disposable nonproduction project, narrow view, dedicated OAuth/KMS/IAM and
  a real read/revoke pilot are still needed before activation.
- Detective case persistence is not established. For now the shareable
  route, fixture revision, saved query and evidence references reconstruct
  the investigation; a durable case is separate follow-up work.

---
*This document follows the https://specscore.md/feature-specification*
