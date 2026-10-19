# ADR: Direct DALGO sources for investigation evidence

Status: local investigation implemented and verified; hosted Google Cloud Logging
code merged but disabled pending a live authorization pilot, 2026-10-19.

## Decision

Use one read-only DALGO adapter for explicitly configured local files and one
for a configured Google Cloud Logging project. A trusted process gives the
driver to OVDB through a generic programmatic read-only mount. Existing OVDB
discovery, query, metadata and result routes are reused. OVDB core contains no
log parser, correlation rule or log presentation logic. DataTug owns the demo's
explicit order field mapping and AG Grid presentation. The Northwind story maps real
order 11039 to synthetic logs. Configurable mapping for a PostgreSQL table of
log-like records is a future UI step, not part of the current demo.

## Alternatives

| Option | Decision | Reason |
| --- | --- | --- |
| Snapshot logs into a database | Rejected | Adds a copy, freshness ambiguity and a storage lifecycle explicitly excluded by the owner. |
| Extend the OVDB protocol with log types or search | Rejected | Couples a database abstraction to logs and changes the stable contract. |
| General provider framework | Deferred | The two concrete sources do not establish a useful extension contract yet. |
| Cloud-only demo | Rejected | Requires external credentials, weakening reproducibility. |

## Boundaries

File paths are fixed in trusted configuration beneath one allowed root.
Parsing is explicit. A bounded scan reads the current file, fails if it changes
during a query, and creates digest-and-byte-range references. Sparse JSONL
fields remain in fields_json. Parsing failures are rows with original raw
content. No rotation history or tail is maintained.

Cloud filters are pushed down with mandatory time and result bounds. The
adapter stores no entries. SDK reconstruction, not wire bytes, is exposed as
raw. A deployment must map each DataTug principal to an explicitly authorized
Google project and enforce that server-side; ADC alone does not do this.

Every adapter rejects writes. The generic OVDB mount refuses keyed and
mutating operations. The OVDB public spec and wire shape remain untouched.
The programmatic mount reports `engine: "external"` through the existing open
string field in discovery. That value is visible to clients. Independent
protocol review confirmed that no route, response field, enum or log-specific
contract changed. The integration test read the existing descriptor by HTTP
GET and queried the collection with an unmodified OVDB Go client. File
manifests still reject `external`.

## Delivery status

- [x] Direct file and Cloud DALGO adapters with bounded queries and unit tests.
- [x] Generic OVDB read-only mount landed in `openvaultdb/openvaultdb-go` PR #88;
  required CI, remote merge and worktree cleanup were verified by WB.
- [x] Existing OVDB descriptor GET and unmodified client query integration test.
- [x] DataTug AG Grid log view and explicit order mapping. Two local Playwright
  tests covered six direct-source entries, log → order route and browser Back.
  The record → logs link and real order fields were observed in the manual
  browser journey and captured in the demo screenshots.
- [x] Local Northwind startup script, versioned fixtures, HTML walkthrough and
  screenshots of the working AG Grid.
- [x] Hosted per-user connection code merged in `datatug-cloud` PR #9. It is
  disabled by default and has no real Google user-grant pilot receipt.
- [ ] Narrow-view IAM, dedicated OAuth/KMS configuration, cleanup-worker
  liveness and a real read/revoke pilot before hosted activation.
- [x] Independent protocol, security, demo and UX review; initialization DDL,
  projection, byte-budget and applied-query identity findings repaired and
  regression-tested. The standalone dependency pin was verified and merged.
- [x] Adapter PR #1 merged at `6973cafaf24fc18da4c8a7172c517017ccc9cf18`;
  demo PR #52 merged at `5d7e9ca39f4ef70b7345b5041a0ef040e133dcf5`.
- [x] DataTug PR #393 merged at `f9f25caf2b3a57e05571946a0a5e1dadf59c4061`;
  its exact head cleared the CI gate with 13/13 checks settled. WB removed the
  remote branch and cleaned the worktree.
- [x] AG Grid DataTug PR #398 and demo screenshot PR #55 merged; independent
  UX/demo and protocol reviews found no remaining blocker. The direct source
  status is tracked in the
  [feature specification](../spec/features/direct-log-evidence/README.md).

The existing Context Bag is scoped to an agent, project, environment and
security context. This local public demo uses a shareable `order=11039` URL
parameter as a minimal context bridge; it does not create or persist a
Detective case or import facts into a signed-in basket. Its route, saved SQL,
fixture revision and evidence references allow reconstruction. Full case
persistence and cross-source Context Bag binding remain product follow-up.
