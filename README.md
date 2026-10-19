# dalgo2logs

Direct, read-only DALGO adapters for log evidence. Nothing is copied to a
database or indexed. This repository has two sources:

| Package | Source | How it reads |
| --- | --- | --- |
| root package | Explicit JSONL and timestamped text files | Bounded scan of current file per query |
| cloudlogging | One configured Google Cloud Logging project | Bounded entries.list request per query |

Both return a normal dal.DB. A trusted process can mount either in OpenVaultDB
through its generic read-only programmatic mount. OVDB receives ordinary
collections, schemas, queries and results. The public OVDB protocol is not
extended with log semantics.

## File source

Configure an ID, trusted root, and named files with explicit formats. JSONL
requires one JSON object per line. The text parser accepts RFC3339 timestamp,
severity, service and message; indented following lines belong to the same
entry. No format guessing, globbing, directory watching or tailing is present.
Traversal, symlinks and nonregular files are rejected. Defaults are 8 MiB per
file, 256 KiB per line or multiline entry, 20,000 entries and 200 results.
Absolute ceilings are 64 MiB, 1 MiB, 100,000 and 1,000 respectively. A scan
fails if a file changes or rotates during the query. A source reference uses
the configured database ID, collection, file content hash and byte range;
references change when content changes.

## Google Cloud Logging source

Configure cloudlogging.Config with a fixed ProjectID, collection, result cap,
time-window cap and query timeout. It uses Application Default Credentials.
Grant the process only the Cloud Logging read permission it needs. Every query
must have an explicit inclusive RFC3339 time range. The default maximum window
is 24 hours, with a seven-day ceiling; the result limit defaults to 200 with
a 1,000 ceiling; timeout defaults to 30 seconds with a five-minute ceiling.
Results are newest first. A reconstructed entry is capped at 1 MiB and a
response at 16 MiB; an oversized response fails instead of dropping evidence.

The adapter accepts equality on severity, service, order_id and
correlation_id. Identifier predicates check matching JSON payload and labels;
correlation_id also checks the provider trace field and service checks the
monitored resource service_name label. Google Cloud's string equality is case
insensitive. The displayed common field prefers payload, then entry labels,
then provider trace/resource metadata. Separate trace_id, labels_json and
fields_json expose differing values. Project selection is never a query input.

Cloud raw is a JSON reconstruction from the SDK entry, not original wire
bytes. A gcl: SHA-256 source_ref hashes the project and reconstructed entry.
Project ID, log name, insert ID and timestamp are separate provenance fields.
Entries without distinct provider IDs can be indistinguishable if identical.

The adapter does not authenticate DataTug users, link Google accounts, or
enforce tenant permissions. A hosted service must authorize each user to a
configured connection before routing any query. Never expose a Cloud Logging
mount from an unauthenticated public server. Logs may contain credentials or
personal data; apply redaction and sharing policy at the owning service.

## Query behavior

Only one declared collection, AND comparisons, supported equality fields,
timestamp bounds/order and a finite limit are implemented. File queries also
accept exact source_ref and parse_status. Unsupported text search, regex,
arbitrary SQL, joins, offsets, aggregation and all writes return
dal.ErrNotSupported. Missing timestamps remain absent, invalid timestamps
are marked invalid_timestamp, and malformed entries retain their raw content.
No timestamp is invented. See docs/architecture.md for the design decision.

The [operator and security notes](docs/operations.md) include the local
Google Cloud Logging setup, byte limits, troubleshooting and measured file
scan cost. The [demo guide](https://github.com/datatug/datatug-demo-project/blob/main/demo-project-1/fixtures/logs/guide.md)
starts the full Northwind investigation.

Run the local tests with:

    GOCACHE=/private/tmp/dalgo2logs-gocache go test ./...

The Cloud tests exercise synthetic SDK entries through the query path,
including filter pushdown and resource errors. They do not make a live
authenticated request to Google Cloud.
