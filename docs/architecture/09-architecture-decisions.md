# 9. Architecture decisions

## ADR-1: Orphan on delete

- **Context:** Structure objects (and rights hanging off them) must not be removed by a stray plan.
- **Decision:** `Delete` only drops state and warns; it never calls ChurchTools. Same semantics as `ct state rm`.
- **Alternatives:** Refusing Delete outright was the first design; it makes un-managing impossible without hand-editing state and breaks `destroy` on a scratch workspace.
- **Consequences:** Guarding against accidental removal moves to plan level (`prevent_destroy`, CI failing on planned deletes, in the consumer). Real deletion is a manual act in ChurchTools.

## ADR-2: Mock verb allowlist

- **Context:** The mock once answered every verb on every path, so a resource built on a verb ChurchTools doesn't serve passed CI (department did).
- **Decision:** `supportedVerbs` per collection; unknown collection 404, unsupported verb 405. Plus `putNoContent` and `putRequired`.
- **Consequences:** Adding a resource requires measuring or reading the spec for its verbs. The allowlist gates on method only; it doesn't prove behaviour ([11](11-risks-and-technical-debt.md)).

## ADR-3: Import by numeric id

- **Decision:** Passthrough import of the ChurchTools id as a string.
- **Consequences:** The exporter emits `import` blocks from ct-cli state; `db_field` and `privacy_agreement_who` are import-only because the objects exist already.

## ADR-4: Optional+Computed without a static default

- **Context:** A static default (e.g. `sort_key = 0`) rewrites an imported row's real value on the next apply.
- **Decision:** Such attributes are `Optional+Computed` with `UseStateForUnknown`; Create substitutes ct-cli's default only for new rows.
- **Consequences:** Omitting an attribute means "keep what ChurchTools has".

## ADR-5: Legacy endpoint only where REST has no write

- **Decision:** Use `churchdb/ajax` solely for tables without a REST write path, behind a table allowlist, column check and session handshake.
- **Consequences:** Id discovery by snapshot-diff, a creation lock, and a duplicate-name refusal for department.

## ADR-6: Separate resources for hierarchy and rulesets (planned)

- **Status:** Accepted. `churchtools_group` (#22) carries only name, type, status and campus; the hierarchy (`churchtools_group_parent`) and ruleset (`churchtools_dynamic_group`) resources are not built yet.
- **Decision:** Hierarchy relations and dynamic-group rulesets are to be separate resources rather than attributes of the group, and ruleset normalisation (label stripping, type coercion) is to be ported into the provider to avoid perpetual diffs.
- **Open:** Details are decided when the group tiers are built; revisit this record then.
