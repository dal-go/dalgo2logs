# Verification record (2026-10-19)

The commands below ran in the `dalgo2logs-investigation` WB worktrees. The
adapter initially used a temporary Go workspace while its generic OVDB mount
was under review. OVDB PR #88 then landed at merge commit
`64e07ec41c4b776bddc0501c042298fb0eb31882`; the adapter pins its Go
pseudo-version and passed a standalone `GOWORK=off` suite.

| Area | Command | Outcome |
| --- | --- | --- |
| OVDB core and server | `go test ./pkg/core ./pkg/server -count=1` | Passed, with local loopback permission: core 0.741s; server 24.894s. Includes the `SchemaModifier` no-DDL regression. |
| Adapter, Cloud, OVDB client | `GOWORK=/private/tmp/dalgo2logs-work/go.work go test ./... -count=1` | Passed, with local loopback permission: file 0.147s, Cloud 0.293s, OVDB client 0.348s. |
| Standalone adapter | `GOWORK=off go test ./... -count=1` | Passed against OVDB `v0.25.8-0.20261019044937-64e07ec41c4b`, with local loopback permission: file 0.246s, Cloud 0.348s, OVDB client 0.477s. |
| Standalone vet | `GOWORK=off go vet ./...` | Passed. |
| Adapter vet | `GOWORK=/private/tmp/dalgo2logs-work/go.work go vet ./...` | Passed before the review fixes; rerun after dependency pin. |
| DataTug timeline | `node_modules/.bin/vitest run --config libs/datatug/main/vite.focused.config.mts libs/datatug/main/src/lib/pages/demo-logs/demo-logs-page.component.spec.ts` | Passed: 3 tests, including applied source identity and carrier-event record return. |
| DataTug library | `NX_DAEMON=false NX_ISOLATE_PLUGINS=false node_modules/.bin/nx build datatug-main --configuration=development --excludeTaskDependencies --skip-nx-cache` | Passed; one unrelated Angular optional-chain warning. |
| DataTug lint | `NX_DAEMON=false NX_ISOLATE_PLUGINS=false node_modules/.bin/nx lint datatug-main --skip-nx-cache` | Passed. |
| Spec | `specscore spec lint --caller codex` | Passed: 0 violations after the review fixes. |
| Browser journey | Built DataTug app at `127.0.0.1:4200`, direct OVDB file server at `127.0.0.1:6832` | Observed order 11039 and `ShippedDate=NULL`, six linked events, raw WARN evidence, severity narrowing and return to the selected order; screenshots are in the demo project's `fixtures/logs` folder. |

Independent review found and prompted fixes for external-mount initialization
DDL, aliased projections, Cloud response-byte accounting and timeline identity.
The same review noted that the adapter's original `v0.25.6` OVDB dependency
could not build standalone because `OpenExternal` was newer. The pin and
standalone test now address that release blocker. Live Google Cloud Logging access was
not exercised; fake-reader tests verify pushdown, bounds, cancellation and
read-only behavior. The local Cloud path is a single-owner connection and is
not a hosted per-user account integration.
