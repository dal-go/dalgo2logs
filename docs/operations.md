# Operator notes and security review

## Local file source

Use an explicitly configured, trusted directory and named regular files.
Never construct a source path from a request, an order ID or a log field.
The adapter rejects traversal and symlinks, holds the allowed root open, and
rescans current bytes on each query. It has no ingestion, index, tail or
rotation history. A query fails if a file changes while it is read. Replacing
the file changes its digest-based source references; old references are not
resolvable after rotation unless the operator retains that original file.

Default bounds: 8 MiB per file, 256 KiB per line or multiline entry, 20,000
entries scanned, and 200 returned. Hard ceilings: 64 MiB, 1 MiB, 100,000 and
1,000. Supported equality predicates are applied during the scan. A
`source_ref` predicate is checked after hashing the full file. The result
limit is applied after timestamp ordering, so it cannot stop the scan early
without changing query meaning. A caller's canceled context aborts a scan.

Run `bash scripts/run-northwind-evidence.sh` from the demo project with the
two local Go checkout paths described in its guide. The server binds only to
loopback. It is intentionally unauthenticated because its input is committed
synthetic data. Do not repoint that public local configuration at real logs.

## Local Google Cloud Logging source

Copy [the example configuration](../examples/cloud-owner.json), replace the
project ID, and use application default credentials for an account with only
the required Cloud Logging read access to that project. Set a strong
`OVDB_LOGS_OWNER_TOKEN` in the environment before starting the server. The
source is fixed to one project in trusted server configuration; a client
cannot select another project through query input. The server refuses Cloud
configuration without the owner token and binds only to loopback. In DataTug,
enter the loopback URL, database, collection and token under **Source
connection**. The page holds the token in memory only and will not send it
to a non-loopback URL.

Every Cloud query requires inclusive RFC3339 lower and upper timestamp bounds.
Defaults: at most 24 hours, 200 returned entries and 30 seconds. Hard
ceilings: seven days, 1,000 entries, five minutes, 1 MiB per reconstructed
entry, 16 MiB scanned and returned, and 10 personal API pages. Configure lower
`MaxEntryBytes`, `MaxScanBytes`, `MaxResultBytes` and `MaxPages` in trusted
hosted state; these defaults are hard ceilings. Scan bytes include local
predicate misses. A budget exhaustion returns `cloudlogging.ErrLimitExceeded`
and `*cloudlogging.LimitError` (with its `Kind` and `Limit`) so the host can
distinguish incomplete results from no matches. The source performs a bounded
`entries.list` call on every query. No entries are stored by this adapter.

This single-owner local path lets a developer inspect a project they control.
It is **not** a multi-user DataTug account link. A hosted integration needs a
server-side principal-to-project authorization decision on every request,
least-privilege Google credentials, revocation and sharing policy before the
same view can show different users their own entries. ADC and a shared owner
token alone cannot establish those boundaries.

## Security decisions and remaining risks

| Surface | Current control | Remaining obligation |
| --- | --- | --- |
| File paths | Trusted root, local relative paths, no symlinks, nonregular files rejected, root handle retained | OS file permissions must restrict the process account; rotate out of band. |
| Query resources | Finite file/entry/result caps, context cancellation; Cloud time/result/duration/byte caps | Deploy a gateway with per-user quotas before hosted use. |
| Mutations | DALGO adapters reject writes; generic OVDB mount and local server advertise/read-only enforce no keyed writes | Keep source credentials read-only too. |
| Sensitive content | Raw lines are evidence and can contain credentials or PII; diagnostics omit payloads and paths | Redact at source or in an authorized server layer; classify and control export and sharing. |
| Cross-user access | Cloud server requires one owner token and loopback | Hosted principal, tenant, project and row/field authorization are not implemented. |
| Prompt injection | UI displays log text as text, not interpreted HTML or instructions | Any future AI explanation must quote retrieved evidence and treat content as untrusted. |
| Source references | File database ID, collection, digest and byte range; Cloud project and reconstructed entry hash | References may disclose source identity; share only with authorized viewers. |

## Performance observation

On 2026-10-19, `go test -run '^$' -bench BenchmarkBoundedFileQuery
-benchtime=3x -count=1` scanned a 2.3 MiB file with 20,000 JSONL entries
and a no-match order filter on an Apple M5 Max. Before streaming predicate
application: 54.1 ms/op, 42.5 MB/s, 77.2 MB allocated/op, 1.48 million
allocations/op. After: 40.9 ms/op, 56.2 MB/s, 46.1 MB allocated/op,
1.10 million allocations/op. These are three-iteration local observations,
not a latency guarantee. A query still scans and hashes the entire bounded
file, and a broad result can retain more data than this selective case.

## Troubleshooting

- **No results:** check the exact identifier, collection and UTC range. The
  demo's malformed and untimed entries have no order ID; clear that filter
  and select the untimed option to inspect them.
- **Unsupported query:** use only the predicates listed in the README;
  SQL, regex, full text, offsets, joins and arbitrary ordering are not
  silently approximated.
- **Source changed during scan:** retry after rotation settles or reduce the
  allowed file size; this adapter does not cache an old copy.
- **401 on Cloud source:** provide the owner token to the loopback DataTug
  connection; do not put it in a URL or commit it to configuration.
- **Browser local SQLite WASM fails only from a managed worktree dev server:**
  a shared `node_modules` symlink outside Vite's serving allow list can cause
  the development server to answer HTML for the WASM request. A fresh local
  dependency install or a built app served with SPA fallback uses the copied
  WASM asset correctly. The record↔log browser journey was verified against
  the built app.
