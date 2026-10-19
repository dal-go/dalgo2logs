# Verification record (2026-10-19)

Most commands below ran in the `dalgo2logs-investigation` WB worktrees; the
focused DataTug browser E2E ran in `dalgo2logs-northwind-e2e`. The
adapter initially used a temporary Go workspace while its generic OVDB mount
was under review. OVDB PR #88 then landed at merge commit
`64e07ec41c4b776bddc0501c042298fb0eb31882`; the adapter pins its Go
pseudo-version and passed a standalone `GOWORK=off` suite.

| Area | Command | Outcome |
| --- | --- | --- |
| OVDB core and server | `go test ./pkg/core ./pkg/server -count=1` | Passed, with local loopback permission: core 0.741s; server 24.894s. Includes the `SchemaModifier` no-DDL regression. |
| Adapter, Cloud, OVDB client | `GOWORK=/private/tmp/dalgo2logs-work/go.work go test ./... -count=1` | Passed, with local loopback permission: file 0.147s, Cloud 0.293s, OVDB client 0.348s. |
| Standalone adapter | `GOWORK=off go test ./... -count=1` | Passed against OVDB `v0.25.8-0.20261019044937-64e07ec41c4b`, with local loopback permission: file 0.246s, Cloud 0.348s, OVDB client 0.477s. |
| Adapter race suite | `GOWORK=off go test -race -count=1 ./...` | Passed after the standalone OVDB dependency pin. |
| Adapter vet | `GOWORK=off go vet ./...` | Passed after the review fixes and dependency pin. |
| DataTug focused suite | `node_modules/.bin/vitest run --config libs/datatug/main/vite.focused.config.mts libs/datatug/main/src/lib/pages/demo-logs/demo-logs-page.component.spec.ts libs/datatug/main/src/lib/ovdb/ovdb-database-browser.component.spec.ts libs/datatug/main/src/lib/pages/demo-sqlite/demo-sqlite-page.component.spec.ts`; `node_modules/.bin/vitest run --config apps/datatug-app/vite.config.mts apps/datatug-app/src/app/datatug-app-routes.spec.ts` | Passed after the CI test repair: 31 library tests (4 timeline, 15 OVDB browser, 12 demo SQLite) and 71 app route tests. The first PR CI test run exposed a missing `demo/logs` route expectation and a demo SQLite ActivatedRoute mock without `queryParamMap`; both test fixtures were corrected. |
| DataTug library | `NX_DAEMON=false NX_ISOLATE_PLUGINS=false node_modules/.bin/nx build datatug-main --configuration=development --excludeTaskDependencies --skip-nx-cache` | Passed; one unrelated Angular optional-chain warning. |
| DataTug app | `NX_DAEMON=false NX_ISOLATE_PLUGINS=false node_modules/.bin/nx build datatug-app --configuration=development --excludeTaskDependencies --skip-nx-cache` | Passed after the final UI fixes; unrelated existing Angular optional-chain and Sass import warnings. The full dependency build was blocked locally by an unrelated release-asset HTTP 404. |
| DataTug lint | `NX_DAEMON=false NX_ISOLATE_PLUGINS=false node_modules/.bin/nx lint datatug-main --skip-nx-cache`; `NX_DAEMON=false NX_ISOLATE_PLUGINS=false node_modules/.bin/nx lint datatug-app --skip-nx-cache` | Both passed. |
| DataTug PR #393 CI | Exact head `745aeb4cdfebbacb777bc277e76f0afac2f6455d` | WB observed 13/13 settled checks, including successful build, test and deploy-gate; the PR merged to main at `f9f25caf2b3a57e05571946a0a5e1dadf59c4061`. Scheduled E2E jobs were skipped by workflow policy, so CI did not replace the manually verified browser journey. |
| Real-source DataTug browser E2E | `DATATUG_DEMO_PROJECT_DIR=/Users/alex/projects/datatug/datatug-demo-project DALGO2LOGS_REPO=/Users/alex/projects/dal-go/dalgo2logs DATATUG_LOGS_E2E_PORT=4342 NX_DAEMON=false NX_ISOLATE_PLUGINS=false node_modules/.bin/nx run datatug-app:e2e -- --config=apps/datatug-app/playwright.logs.config.ts --forbid-only` from the DataTug app checkout, after the documented development build | Passed 2/2 on the reviewed PR #395 head. The test starts the real direct OVDB adapter against synthetic files, exercises six events, source/raw and multiline evidence, severity and identifier filtering, an empty result, the order-record URL, and browser Back. It creates a temporary CORS config and stops its owned adapter process. Default Playwright discovery excludes this opt-in fixture-dependent suite (155 tests in 42 files listed without external checkouts). |
| DataTug PR #395 CI and landing | Head `dad03393b455b2b1317514e4226c6193b9502204`; `wb pr land datatug/datatug-apps#395` | WB reported 14/14 settled checks and successful landing; GitHub independently reported `MERGED` at `6947104607ffa8447c88a020897755a3388700a7`. Build, test and deploy-gate passed; scheduled E2E jobs were skipped by workflow policy. The branch and WB worktree were cleaned. |
| Spec | `specscore spec lint --caller codex` | Passed: 0 violations after the review fixes. |
| Browser journey | Built DataTug app at `127.0.0.1:4200`, direct OVDB file server at `127.0.0.1:6832` | Observed order 11039 and `ShippedDate=NULL`, six linked events, raw WARN evidence, severity narrowing and return to the selected order; changing the order to 11040 updated the URL and yielded one unrelated event. The 375px filter and expanded timeline were manually observed without horizontal overflow; durable screenshots cover the desktop journey in the demo project's `fixtures/logs` folder. |

Independent review found and prompted fixes for external-mount initialization
DDL, aliased projections, Cloud response-byte accounting and timeline identity.
The same review noted that the adapter's original `v0.25.6` OVDB dependency
could not build standalone because `OpenExternal` was newer. The pin and
standalone test now address that release blocker. Live Google Cloud Logging access was
not exercised; fake-reader tests verify pushdown, bounds, cancellation and
read-only behavior. The local Cloud path is a single-owner connection and is
not a hosted per-user account integration.

The focused browser E2E asserts the order record route and return navigation,
but does not verify completion of the pinned browser SQLite download or the
selected record's fields; those were observed in the separate manual browser journey.
It also does not create a persisted Detective case.
