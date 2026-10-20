# terraform-provider-churchtools

An OpenTofu/Terraform provider for [ChurchTools](https://church.tools) structure:
campuses, group types, departments, person statuses, groups and permissions.

It manages the *scaffold* only. People, memberships and participants are out of
scope and are never read or written.

Resource type names carry the provider prefix (`churchtools_campus`), as
Terraform and OpenTofu require: the framework derives every type name from the
provider name.

```hcl
resource "churchtools_campus" "mainz" {
  name   = "Mainz"
  shorty = "MZ"
}
```

## Status

Pre-release. Tier-0 master data only: campus, group type, department, person
status, comment viewer, contact label, relationship type, privacy-policy
agreement type, privacy-agreement "who", target group, age group and group
category — plus `db_field`, which manages one setting of an existing data field, and
`group` (name, group type, status and campus of a group; hierarchy, dynamic groups,
member fields and grants will be separate resources).

One tier-2 resource is in as a **spike**: `group_role`, the grants one role holds
in one group, with `preserve_unknown` for partially managed grant lists. It is
covered by unit tests and the mock only; see below before using it on a real
instance.

Nothing is ever deleted in ChurchTools. Every resource's `Delete` **un-manages**
the object instead: it drops it from state, leaves it untouched in ChurchTools,
and emits a warning that points the operator to the ChurchTools UI if it should
really go.

## Install

```hcl
terraform {
  required_providers {
    churchtools = {
      source  = "eqrm/churchtools"
      version = "~> 0.1"
    }
  }
}
```

Until the first signed release is published and registered, this resolves to
nothing and `tofu init` fails — see _Releasing_ below for what is still
outstanding. Build it locally in the meantime (_Local development_).

## What is verified, and what is not

Token mode's single login and its renewal are verified against the mock only.
Mid-run session expiry has never been exercised against a live instance, nor
has REST with a cookie the provider bought itself (session mode has run live
with a ct-cli cookie). The mock models expiry as 401 on REST and as `200
{"status":"error","message":"Session expired!"}` on the legacy endpoint, which
the provider renews once. ChurchTools answers a lapsed session on `/groups`
with 403, not 401, so there is no renewal there and the read fails closed.

Worth stating plainly, because "it has tests" and "its writes work" are different
claims here.

Every type's **read** path is exercised against a live ChurchTools instance, and
the acceptance tests cover all eight resource types against an in-process mock. The
**write** path is the interesting one: `campus`, `group_type`, `person_status` and
`comment_viewer` go through the REST API, but `department` (Bereich) has no REST
write path at all and goes through ChurchTools' legacy master-data endpoint
(`churchdb/ajax`, `cdb_bereich`) — which needs a session handshake rather than the
token header, returns no id on create, and accepts an unknown column silently.

`contact_label` (`/contactlabels`) and `relationship_type` (`/person/relationshiptypes`)
are plain REST with full CRUD; their field contracts were taken from a live instance's
OpenAPI spec (CT 3.137).

`privacy_agreement_type` has **no** REST endpoint and goes through the same legacy
master-data endpoint as `department`, on table `cdb_privacy_policy_agreement_types`
(reads: the `getMasterData` row set `privacy_policy_agreement_types`). The write is the
call the ChurchTools admin UI itself makes — `cc_maintainstandardview.js` →
`renderEditEntry` posts `{func: "saveMasterData", table, id, col0/value0…}` through
`churchInterface.jsendWrite` — with the same session + CSRF handling departments use.
**This write path has not been exercised against a live instance yet**; it is covered
by the mock, and its first real run is ct-structure's CI apply on eqrm-dev.

That legacy path was verified end to end against a live instance on 2026-09-21:
`name`, `shorty` and `sort_key` all land, each read back through a separate client
rather than through the provider that wrote it. A Bereich **create** has never been
exercised (nothing needed one) and a **delete** never will be, since Delete only
un-manages by design.

`db_field` (`/dbfields/{id}`) is **import-only** and manages two settings of a data
field that already exists: `is_new_person_field` ("Beim Anlegen einer Person
abfragen") and the optional `is_active` ("Aktiv" — left out, the instance's value
is kept); `key` is read-only. A built-in field can be inactive: eqrm-dev ships
`isSystemUser` that way. Create refuses and points at an `import` block. The
write contract was measured on eqrm-dev (CT 3.137.0-RC22) on 2026-09-29: there is no PATCH
(405, "Must be one of: GET, PUT, DELETE"), and a partial PUT is a 400 because PUT
replaces. Update therefore GETs the field and PUTs back the OpenAPI request keys
(`id, name, shorty, length, lineEnding, securityLevel, sortKey, isActive,
useAsPlaceholder, isNewPersonField, deleteOnArchive`) with only the flag changed. On
dev that body was a verified no-op on three fields (GET before/after identical), and a
real toggle of field 32 landed and was restored. `is_active` rides the same PUT.
Its first live write, on eqrm-dev on 2026-10-02, showed that CT locks "Aktiv" on some
built-ins: `isActive = false` on birthplace (18), job (19) and nationalityId (20)
answered 200 and changed nothing, and CT's own UI greys the checkbox out. Nothing in
the GET marks the lock, so Update reads the field back after every PUT and fails with
„ChurchTools hat die Änderung ignoriert“ instead of storing a value the instance
doesn't hold. The read-back ran live on eqrm-dev on 2026-10-02: `is_active = false`
on job (19) failed with that error and left field and state at `true`, and an
`is_new_person_field` toggle of field 32 passed it both ways and was restored. A
successful `is_active` toggle has not run live yet.

`group` (`/groups/{id}`) reads the group's ids from `information` and writes them flat
(`name`, `groupTypeId`, `groupStatusId`, `campusId`). Verified read-only on eqrm-dev on
2026-10-16: the GET shape (`campusId` is always present, JSON null or an int) and the
404 for an unknown id. Verified live on eqrm-dev on 2026-10-16 with one disposable group,
deleted afterwards: POST creates and returns the id, PATCH answers 200, `campusId: null`
clears a campus, a rename and a campus change each plan a no-op afterwards, and import
of the existing group plans a no-op. Update still doesn't trust the reply: it reads the
group back and fails with „ChurchTools hat
die Änderung ignoriert“ if any of the four values differs from the plan, so a silent
ignore is an error rather than drift. Delete only un-manages; CT's DELETE would 409 on
references and destroy memberships.

A `group_type_id` change is a migration: CT answers `PATCH groupTypeId` with
`400 groupTypeId: validation.always.invalid`, so Update leaves the field out of the PATCH
and calls `POST /groups/{id}/grouptype` with a `roleMapping` over every role of the old
type (ids from `/group/roles`; the design and the live verification are ct-cli's,
`src/engine/grouptype.ts`). A role maps to the same-named role of the new type, or, if
nobody holds it in this group, to the new type's default role. A role that holds members
and has no same-named target fails plan and apply with a German error naming it. The members read asks for all four `group_member_statuses[]` (active, requested, to_delete, waiting) explicitly; eqrm-dev's default listing includes `to_delete`, but `requested` and `waiting` have not been verified live. A same-name match that turns a leader role into a participant role (or back) is kept but marked in the plan warning. **Not
verified against a live instance from this provider**, and whether CT keeps a group's
per-role grants (`churchtools_group_role`) across the migration is open.

`group_role` (`/permissions/group_role/{pairingId}`, pairing resolved through
`/groups/{id}/roles`) ran live on eqrm-dev on 2026-10-19 (sll_koblenz Mitglied,
pairing 16829, [protocol and result in #26](https://github.com/eqrm/terraform-provider-churchtools/pull/26#issuecomment-6263338890)):
create without writes, an undeclared grant on a preserved dimension stable over three
plans, an undeclared unscoped right revoked alone, a declared selection row added and
revoked by exact `dataId`. Measured there: a scoped PUT is additive and an exact-id
DELETE removes only that row. **Not measured**: DELETE without `dataId` (the mock
removes every selection row of the right), a selection PUT next to `-1` (the mock
ignores it), and whether a DELETE also removes a `revoke` row of the same identity;
these come from CT 3.137's OpenAPI text. The skip of rights the host does not offer
(701/702/2080 on eqrm-dev) is mock-tested; its right table (`getMasterData`) was
probed read-only on eqrm-dev and eqrm (2026-10-19), but no apply has skipped a right
live yet. Payload fixtures were captured read-only from
eqrm-dev (`internal/provider/testdata/group_role/`). See
[group_role](#group_role-and-preserve_unknown) for the semantics.

## group_role and preserve_unknown

`churchtools_group_role` holds every grant of one role in one group ("Rechte
verwalten" at a group). It creates nothing: CT pairs every role of the group type
with the group when the group is created, and the resource writes onto that
pairing. Example: [`examples/resources/churchtools_group_role`](examples/resources/churchtools_group_role/resource.tf).

- **One `grant` block per right**, with the ids in the argument for the right's
  scope dimension (`groups`, `campuses`, `departments`, `security_levels`,
  `group_types`, `comment_viewers`; every other dimension in `data_ids`). A block
  without ids is the right's row without a selection, which for a scoped right is
  *not* "all"; "all" is `["-1"]`. Rights are named as in ct-structure
  (`module:name`), resolved through a catalog embedded from eqrm.church.tools
  (CT 3.137.1).
- **`applies_to_subgroups = true`** is CT's `isInherited`: the row applies in the
  group's subgroups, not in the group itself. It is a separate grant from the
  direct one; write both if both are meant. Leave it out rather than writing false.
- **Undeclared grants are revoked**, like any Terraform list. Exceptions, never in
  state and never revoked: deny rows (`type: revoke`), CT's own system rows
  (`modifiedPid -1`), rights the catalog does not know (warned about), and
  **undeclared grants on a dimension named in `preserve_unknown`**. Those last ones
  are listed in the computed `preserved_grants`.
- `preserve_unknown` takes CT scope-field names (`crp_query`, `cc_html_template`,
  `cdb_gruppe`, …), as ct-structure's `preserveUnknown` does. Unscoped rights have
  no dimension and are never preserved. A grant you *declare* on a preserved
  dimension is owned: removing its block revokes it, by exact id.
- An apply only ever revokes a row the plan showed (one in state). Adding a
  dimension to `preserve_unknown` keeps the rows on it that the same plan showed as
  removals; removing a dimension makes its undeclared rows appear as removals in
  the *next* plan, never in the same apply.
- Every write is read back. CT answers 204 to a grant it does not store (for
  instance a selection next to an existing `-1`), and the apply fails instead of
  writing a state that lies.
- **Rights the host does not offer are skipped, with a warning.** eqrm-dev lacks the
  churchreport and Flow 2.0 preview modules, so for 701/702 and 2080 CT answers 204
  and never returns the row. The HCL stays the same on every host; the provider asks
  the host itself which rights it defines (`POST /index.php?q=churchauth/ajax`,
  `func=getMasterData`, `data.auth_table`: the same right table ct-cli captures per
  host, read once per provider run through the session or token you already
  configured). The embedded catalog is shared by all hosts and cannot answer this.
  A declared grant whose right is not in that table is never written, never read
  back and never revoked; it stays in state as declared, so the plan is clean, and
  every apply or refresh warns with role, right and host. The table is only asked for
  when a declared grant is not live yet (once per run). A right the host *does*
  offer keeps the strict read-back failure above. If the table cannot be read,
  nothing is skipped: every grant takes the strict path, so grants the host stores
  still apply, and one it does not store fails with the reason in the message.
- `preserved_grants` is what the last refresh saw, written to state like any
  attribute: a plain `plan` refreshes in memory and stores nothing, so a grant
  added in the UI shows up there after the next `apply` (or `apply -refresh-only`).
  A plan that changes grants keeps it as it is, unless the change can move rows in
  or out of it (a changed `preserve_unknown`, or a block declaring a preserved row);
  only then is it "known after apply".
- `terraform destroy` un-manages and revokes nothing. To revoke, remove the blocks
  and apply first.
- Import id: `<group id>/<role name>`, or `<group id>/<role name>/<dim>,<dim>` to
  carry `preserve_unknown` into the import so preserved rows never enter state.

## Authentication

Two ways, and the second is preferred.

### A session (recommended)

`session_cookie` + `csrf_token` authenticate with a ChurchTools *session*, which
expires on its own. `ct auth token` emits one as JSON on stdout and nothing
else, so a `data "external"` block feeds it straight in and the login token
never leaves ct-cli's Keychain:

```hcl
data "external" "ct_session" {
  program = ["ct", "auth", "token", "--env", "prod"]
}

provider "churchtools" {
  host           = "https://example.church.tools"
  session_cookie = data.external.ct_session.result.cookie
  csrf_token     = data.external.ct_session.result.csrfToken
}
```

Pass `--env` so the token is for the instance you mean. (Before ct-cli 3.10.1,
`--env` was also the workaround for `environment: null`, which `data "external"`
rejects because every value must be a string. Since eqrm/ct-cli#184 the field is
`""` when no environment is selected.)

Sessions expire, and ChurchTools does not advertise how long they last — the
`expiresAt` ct-cli reports is a ceiling, not a promise. The provider cannot
renew a session it was handed, so an expired one fails with a message saying to
run again; the helper then fetches a fresh session. Calling it on every run is
the intended usage and is normally free, because ct-cli caches the session.

### A login token

`token` still works and is what CI uses, where the token already comes from a
secret store. Be aware of what it is: permanent, impossible to scope, and on a
production instance an administrator credential. It cannot be rotated without
invalidating every other use of it, so a copy in a `.tfvars`, a CI variable or a
`TF_LOG=DEBUG` transcript outlives whoever put it there. That asymmetry is the
whole reason the session mode exists.

In token mode the provider logs in **once per run**: it exchanges the token for a
session (`GET /api/whoami?login_token=…`, then `GET /api/csrftoken`) on the first
request, shares that session between all resources, and sends it as a cookie
(plus the CSRF header on writes) instead of repeating `Authorization: Login` on
every call, which ChurchTools logs as one login per request. If the session
lapses mid-run, the provider logs in again once and retries the refused request
once; a login answered with 429 is retried with `Retry-After` (bounded, a few
attempts) and then fails with a clear error. The token never appears in
diagnostics. Session mode never logs in.

Set exactly one of the two. Supplying both is an error rather than a precedence
rule: an author who thinks the credential they just added is the live one, and
is wrong, authenticates as the wrong identity with nothing to tell them so.

## Local development

Build and point OpenTofu at the local binary:

    go build -o ~/go/bin/terraform-provider-churchtools

Then in `~/.tofurc`:

    provider_installation {
      dev_overrides { "eqrm/churchtools" = "/Users/<you>/go/bin" }
      direct {}
    }

With a dev override, `tofu init` is skipped — run `tofu plan` directly.

## Tests

    go test ./...            # unit tests
    TF_ACC=1 go test ./...   # plus acceptance tests against an in-process mock

No test touches a live ChurchTools instance.

## Releasing

A `vX.Y.Z` tag triggers `.github/workflows/release.yml`, which builds every
platform with goreleaser, writes `_SHA256SUMS`, and GPG-signs that document. The
registry serves those assets verbatim, so the asset names in `.goreleaser.yml`
are load-bearing rather than cosmetic.

Three things must be in place first, and none of them can be done from a
pull request:

1. **A signing key.** Create it, then set `GPG_PRIVATE_KEY` (ASCII-armoured
   private key) and `PASSPHRASE` as repository secrets. The release workflow
   fails early and says so if they are missing — an unsigned release is worse
   than no release, because the registry would advertise a version that every
   `tofu init` then refuses.
2. **That key registered with the OpenTofu registry** for the `eqrm` namespace.
   The registry only accepts a key from a **public** member of the organisation,
   so whoever submits it has to make their `eqrm` membership public first.
3. **A registry submission** — an issue on
   [`opentofu/registry`](https://github.com/opentofu/registry/issues/new/choose),
   which automation validates and turns into a pull request. The repository name
   must be exactly `eqrm/terraform-provider-churchtools`, which it is.

Until step 2 is done, releases build and sign correctly and are still unusable.

## Documentation

Architecture (arc42): [docs/architecture](docs/architecture/README.md)

## Licence

[MPL-2.0](LICENSE) — the licence OpenTofu itself and every HashiCorp provider
use.
