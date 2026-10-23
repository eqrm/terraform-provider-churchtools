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

## ADR-6: Separate resources for hierarchy and rulesets

- **Status:** Accepted; `churchtools_dynamic_group` is built (v0.10.0). The hierarchy resource (`churchtools_group_parent`) is still to do.
- **Decision:** `churchtools_group` carries only name, type, status and campus. The dynamic rule of a group is its own resource, `churchtools_dynamic_group`, so a ruleset error cannot take the group (and its type migration) with it.
  - **Attributes:** `group_id` (RequiresReplace, import id), `status` (`active` | `manual`, no `none`), `ruleset` (JSON string), `refs` (map of strings).
  - **Ruleset stays a JSON file.** Host-specific ids are `{"__ctRef":true,"kind":...}` markers (`group`, `campus`, `group-type`, `group-type-role`, `person`). The provider replaces each with the integer id from `refs` (`<kind>:<key>`, `group-type-role:<type>/<role>`), in `query` and in `process.*.handleMembership.groupTypeRoleId`. A marker without an entry, or an entry without a marker, fails at validate time. Unknown ref values (a group created in the same run) defer. The refs values are real references, so they are graph edges. The same file also works with ct-cli, which keeps the way back open.
  - **Normalizer** (`internal/ruleset`): a 1:1 Go port of ct-cli's `normalizeRuleset` (unwrap `[RuleSet]` and `{dynamicGroupRuleSet:[..]}`, drop the update timestamps, and only inside `query` strip cosmetic `dterm` labels by allowlist and coerce canonical integer strings up to 2^53-1). Decoded with `UseNumber`. Parity with ct-cli is tested against goldens made by ct-cli itself (anonymized fixtures in the repo, the full live corpus locally via `CT_RULESET_CORPUS`).
  - **No perpetual diff:** `ruleset` has a custom type whose `StringSemanticEquals` compares normalized trees, and `ModifyPlan` keeps the state text when the configured ruleset with its markers resolved normalizes to the live one. State may hold the live JSON.
  - **Writes:** `PUT /dynamicgroups/{id}/ruleset` with `{"dynamicGroupRuleSet":[rs]}` only when the normalized ruleset differs from the live one, then `PUT .../status`. Never `POST /refresh`; ChurchTools re-evaluates on its own.
  - **Only differences are written.** Create, update and import all compare with the live state first: the ruleset is PUT only when the normalized trees differ, the status only when it differs. A refs-only change (e.g. the first plan after an import, where state has no refs) therefore plans an update but writes nothing; OpenTofu rejects a plan that drops a configured `refs` value against a null prior, so the plan cannot be made empty. For `manual` the status is written before the ruleset, for `active` after.
  - **Adoption is explicit.** Create refuses a group whose live rule differs from the configured one ("erst importieren"); an equal one is adopted without a write. Import refuses a group without a ruleset, so a wrong id cannot promote a plain group; promoting by creating the resource without an import stays possible.
  - **Delete is state-only** with a warning: the rule keeps running in ChurchTools.
- **Consequences:** Removing the resource from the configuration does not stop the rule. Reads work for archived groups. A ruleset 404 means "no ruleset" and plans a PUT; any other read error fails the plan.
