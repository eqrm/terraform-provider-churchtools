# 10. Quality requirements

| Goal | Scenario | Evidence |
|---|---|---|
| Zero-change import | Importing every row of an exported state, including id 0, yields an empty plan, and again without the import blocks. | `TestGolden_*` in `golden_test.go`, synthetic fixtures in `testdata/` |
| Never delete | Destroying any resource sends no request to ChurchTools and warns. | Every `Delete` only adds the warning (review). Acceptance tests assert the row survives for department and db_field only so far. |
| Mock fidelity | A resource using an unsupported verb fails in CI; a PUT body missing required keys is a 400; a 204 PUT is handled. | `testmock` allowlists and tests |
| Safe credentials | Half a session or two credentials is refused at configure time. | `provider_test.go` |
| No crash on deferred config | Unknown credentials at plan time yield a German diagnostic. | `notconfigured_test.go` |
| Honest writes | A write ChurchTools silently ignores fails with an error instead of storing a value it doesn't hold. | `db_field` read-back after PUT |
| Complete lists | A collection longer than one page is read fully. | `client_test.go` |
| Release safety | A tag is only cut on explicit decision; release config is checked in CI. | `goreleaser check`, [07](07-deployment-view.md) |
