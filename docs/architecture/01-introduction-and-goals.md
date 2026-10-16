# 1. Introduction and goals

## Purpose

An OpenTofu/Terraform provider for the *structure* of a ChurchTools instance:
campuses, group types, departments and the other master-data tables, later
groups and rights. People, memberships and participants are out of scope and are
never read or written ([README](../../README.md)).

The provider replaces the TypeScript config and state file of the in-house
`ct-cli` for the rights-bearing structure. OpenTofu then supplies what ct-cli
re-implemented: state, dependency graph, plan/apply and drift detection.
ct-cli stays for discovery (`adopt`), coverage checks and the exporter that
feeds the cutover.

## Requirements in short

| Requirement | Where |
|---|---|
| One resource type per ChurchTools master-data table, with import | [05](05-building-blocks.md) |
| Existing objects are adopted by `import` without any change | [06](06-runtime-view.md) |
| Removing a resource never deletes anything in ChurchTools | [09](09-architecture-decisions.md) ADR-1 |
| Publishable to the OpenTofu registry | [07](07-deployment-view.md) |

## Quality goals

| Priority | Goal | Meaning |
|---|---|---|
| 1 | Zero-change import | Importing an object that ct-cli exported yields an empty plan. A field that reads back differently than it was written is a bug. |
| 2 | Never delete in ChurchTools | `Delete` only drops state. |
| 3 | Mock fidelity | The in-process mock must refuse what a live instance refuses; a green suite must mean more than "agrees with the mock". |

Concrete scenarios: [10](10-quality-requirements.md).

## Stakeholders

| Role | Interest |
|---|---|
| ChurchTools admins | Readable HCL, German plan output and errors |
| Maintainers | Small surface, safe releases |
| Consumer repo `ct-structure` | Stable schemas, because a published version can't be taken back |
