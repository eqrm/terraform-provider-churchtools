# 2. Constraints

*Short section.*

| Constraint | Consequence |
|---|---|
| Go, `terraform-plugin-framework` (protocol v6) | Binary-compatible with OpenTofu and Terraform. |
| Resource type names carry the provider prefix (`churchtools_campus`) | The framework derives type names from the provider name; bare names are impossible. |
| ChurchTools REST API is incomplete | Some tables (department, privacy-agreement type) have no REST write path; see [ADR-5](09-architecture-decisions.md). |
| The legacy `/index.php` endpoint is undocumented and not in the OpenAPI spec | Writes through it are allowlisted and column-checked. |
| Public repository, MPL-2.0 | No real instance data in fixtures or docs; test fixtures are synthetic (`testdata/`). |
| German for operator-facing text; English for code, attributes, docs | Diagnostics are German, see [08](08-crosscutting-concepts.md). |
| Registry layout | Repository name `eqrm/terraform-provider-churchtools`, signed `_SHA256SUMS`, `terraform-registry-manifest.json`. |
