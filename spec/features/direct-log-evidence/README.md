---
format: https://specscore.md/feature-specification
status: Draft
---

# Feature: Direct log evidence sources

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/dal-go/dalgo2logs/spec/features/direct-log-evidence?op=explore) | [Edit](https://specscore.studio/app/github.com/dal-go/dalgo2logs/spec/features/direct-log-evidence?op=edit) | [Ask question](https://specscore.studio/app/github.com/dal-go/dalgo2logs/spec/features/direct-log-evidence?op=ask) | [Request change](https://specscore.studio/app/github.com/dal-go/dalgo2logs/spec/features/direct-log-evidence?op=request-change) |
**Status:** Draft
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
DataTug maps ordinary fields to a timeline view and a known order ID to
the related record. The Northwind fixture pairs real order 11039 with
clearly synthetic log entries, and never claims historical causation.

## Acceptance Criteria

- A fresh local setup reads both file formats directly and a normal OVDB
  client discovers and queries the collection through existing routes.
- Every file query enforces a trusted root, entry/file/result caps and
  cancellation. Cloud queries enforce project, time window, result and
  duration bounds. No log entry is ingested into a database.
- Unsupported query shapes and writes fail explicitly. Missing and malformed
  timestamps remain visible without fabricated values.
- DataTug opens order 11039, follows its explicit order_id mapping to
  chronological evidence, opens raw/multiline details, and returns to the
  order. The documented conclusion cites both record and synthetic log facts.
- Tests cover file, Cloud filter construction, ordinary OVDB client access,
  failure modes and UI states. A standalone HTML walkthrough explains the
  same implemented path.

## Open Questions

- Does the existing OVDB metadata engine string permit the private value
  `external` for a programmatic mount? A compatibility review is required
  before release; the public protocol and spec must not be expanded silently.
- Hosted Google Cloud Logging requires a server-side per-user connection
  authorization design. The local single-owner adapter does not establish
  DataTug user or tenant rights.
- Detective case persistence is not established. For now the shareable
  route, fixture revision, saved query and evidence references reconstruct
  the investigation; a durable case is separate follow-up work.

---
*This document follows the https://specscore.md/feature-specification*
