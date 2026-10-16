# 4. Solution strategy

| Goal | Approach |
|---|---|
| Replace ct-cli for structure | One thin CRUD resource per table, typed by per-resource schemas over an untyped `client.Row`. |
| Zero-change import | `import` by numeric id (passthrough into `id`); `Optional+Computed` attributes without static defaults so a config that omits a value keeps CT's value ([ADR-4](09-architecture-decisions.md)). Proven by the golden test. |
| Never delete | Delete is orphan-on-delete: state is dropped, a German warning is emitted, CT is untouched ([ADR-1](09-architecture-decisions.md)). Accidental removal is guarded at plan level by `prevent_destroy` and a CI gate in the consumer, which fails on any planned delete. |
| Honest mock | `supportedVerbs`, `putNoContent`, `putRequired` encode measured live behaviour ([ADR-2](09-architecture-decisions.md)). |
| Tables without REST write | Legacy master-data endpoint with session handshake, table allowlist and column check. |

## Migration tiers

The consumer migrates from ct-cli in tiers; stopping at any tier is a legitimate
resting place. The tier numbering comes from ct-cli's registry.

| Tier | Content | State in this repo |
|---|---|---|
| 0 | Master data: campus, group type, department, person status, comment viewer, age/target group, relationship type, ... | Implemented (see [05](05-building-blocks.md)) |
| 1 | Groups | Out of scope of this documentation |
| 1.5 | Group member fields (must migrate before groups leave ct-cli state) | Planned |
| 2 | Roles and grants, gated on a spike for partially managed lists | Planned |

Per type the cutover loop is: export, `tofu plan` shows imports only and zero
changes, apply, remove the type from ct-cli, verify ct-cli still plans a no-op.
