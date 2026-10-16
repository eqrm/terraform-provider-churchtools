# 12. Glossary

| Term | Meaning |
|---|---|
| Bereich | ChurchTools department. Written through the legacy endpoint (`cdb_bereich`); resource `churchtools_department`. |
| Standort | Campus (`churchtools_campus`). |
| Gruppentyp | Group type (`churchtools_group_type`). |
| Gruppenkategorie | Group category. |
| Zielgruppe / Altersgruppe | Target group / age group. |
| Personenstatus | Person status. |
| Datenfeld | Data field on a person (`db_field`); "Aktiv" is its `is_active` flag. |
| Stammdaten | Master data: the configuration tables of ChurchTools. |
| Legacy master-data endpoint | `/index.php?q=churchdb/ajax` with `getMasterData` / `saveMasterData`; not REST, not in the OpenAPI spec. |
| Orphan on delete | Delete drops state and leaves the object in ChurchTools. |
| `prevent_destroy` | Terraform lifecycle flag that fails a plan containing a delete; used by the consumer. |
| Tier | Migration stage from ct-cli (0 master data, 1 groups, 1.5 member fields, 2 roles/grants). |
| Golden test | Zero-change import proof against synthetic exporter state. |
| ct-cli | In-house CLI for ChurchTools; source of the exporter and of sessions. |
| ct-structure | Private repo holding the HCL configuration that consumes this provider. |
| Session | Cookie plus CSRF token, as emitted by `ct auth token`; alternative to a login token. |
