# 11. Risks and technical debt

| Risk | Impact | Mitigation |
|---|---|---|
| **The mock is not ChurchTools.** A green suite proves agreement with the mock only. | Bugs that appear only live (it once hid a PUT-with-body vs 204 difference). | Mock fidelity rules ([08](08-crosscutting-concepts.md)), probes on a development instance. |
| **Unverified live paths.** Some writes have run only against the mock. | First real apply may fail. | The record of what has and hasn't run live is the README section [What is verified, and what is not](../../README.md#what-is-verified-and-what-is-not); not duplicated here. |
| Legacy endpoint is undocumented | A ChurchTools update can break writes without notice; unknown columns are accepted silently. | Allowlist, column validation, live re-probe before widening. |
| Session lifetime unknown, no renewal | Long runs may fail with `ErrSessionExpired`. | Re-run; the credential helper fetches a new session. |
| A tag can't be undone | A wrong schema is public. | Tag only on explicit decision; check for a parallel release. |
| API churn | Each ChurchTools change can cost a provider release. | Small surface; field contracts taken from the OpenAPI spec. |
| Release prerequisites (signing key, registry key, submission) are manual | Releases build but `tofu init` refuses them. | README "Releasing". |
| Partially managed grant lists have no Terraform equivalent | Rights could vanish without a delete. | Gated on a spike in the consumer's design; not in this repo yet. |

Technical debt: the mock's verb table gates on method alone; `gofmt` isn't in CI.
