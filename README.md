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

Tier 3, part 1 adds three more, on the same grants engine (`internal/grants`,
shared with `group_role`): `group_role_definition` (the role of a group type
itself, `/group/roles`), `group_type_role` (its type-level grants,
`/permissions/group_type_role/{roleId}`) and `person_status_grants` (the `status`
permission domain). Their basic write paths ran live on dev once; see below.
`dynamic_group` (the ruleset and status of one group, with `__ctRef` markers resolved
through a `refs` map) is the last piece of tier 3; see the example in
[`examples/resources/churchtools_dynamic_group`](examples/resources/churchtools_dynamic_group/resource.tf).

Nothing is ever deleted in ChurchTools. Every resource's `Delete`
**un-manages** the object instead: it drops it from state, leaves it untouched
in ChurchTools, and emits a warning that points the operator to the ChurchTools
UI if it should really go.

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

The 429 handling (request pacing, Retry-After, shared pause) is verified only
against `httptest` servers in `internal/client/pacer_test.go`; the mock has no
rate limit. Live ChurchTools does answer 429 under a parallel apply, but whether
it ever applies a throttled POST before refusing it is assumed (as ct-cli does),
not tested. If it did, a retried create could duplicate a row.

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

`dynamic_group` (`/dynamicgroups/{id}/ruleset` and `/status`) has **read** live, writes
against the mock only. On eqrm-dev on 2026-10-23 an import and plan of two existing rules,
one verbatim and one with five ids swapped for `__ctRef` markers plus `refs`, planned no
ruleset or status change (the marker one adds `refs`, which import cannot know). The same
probe measured the bodies the mock now copies: both GETs answer bare JSON without a `data`
envelope; a group without a rule answers 404 on ruleset and `{"dynamicGroupStatus":"none"}`
on status; an unknown group answers 404 on both. Its normalizer is a port of ct-cli's `normalizeRuleset` and is checked byte for byte
against goldens that ct-cli's own code produced: the 31 anonymized rulesets and 8 hand-made
edge cases in `internal/ruleset/testdata` run in CI, and a local-only run over a full corpus of
raw live GET bodies (`CT_RULESET_CORPUS=<dir> go test ./internal/ruleset`) matched every file
when the resource was written. Not verified live: that a PUT of the normalized ruleset lands
and reads back equal, the reply status and body of the two PUTs (the provider ignores both). Writes are minimal by design: ruleset and status are only PUT when they differ from live,
Create refuses to overwrite a different live rule, and Import refuses a group without a rule.
The write path
needs a run against a disposable group on dev before the first adoption.

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
and has no same-named target fails plan and apply with a German error naming it. The
members read asks for all four `group_member_statuses[]` (active, requested, to_delete,
waiting) explicitly; eqrm-dev's default listing includes `to_delete`, but `requested` and
`waiting` have not been verified live. A same-name match that turns a leader role into a
participant role (or back) is kept but marked in the plan warning. Ran live on eqrm-dev
on 2026-10-20 from this provider: an empty probe group (2041) went from type 5 (Team) to
21 (Local Lead) and back, both `POST /grouptype` calls were accepted, the follow-up plan
was empty and the plan warning listed the mapping. **Not verified live:** a migration
with members in the group, and whether CT keeps a group's per-role grants
(`churchtools_group_role`) across it.

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
eqrm-dev (`internal/grants/testdata/`). See
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

## Tier 3: role definitions, type-level grants, status grants

- `churchtools_group_role_definition` (`/group/roles`, import id = role id) manages
  `name`, `type`, `shorty`, `sort_key`, `is_default` and `is_hidden`. CT's PUT is
  not partial and `nameTranslated` is read-only, so an update reads the live row,
  overlays the planned values and sends the full object; `growPathId` is passed
  through, never written. `group_type_id` cannot change: a change is a plan error,
  not a replacement. Destroy un-manages: the role is dropped from state with a
  warning and stays in ChurchTools; delete it by hand in the UI (ct-structure#116).
- `churchtools_group_type_role` (`/permissions/group_type_role/{roleId}`, import id
  = role id) has the same `grant` block, `applies_to_subgroups`, `preserve_unknown`
  and `preserved_grants` as `group_role`. CT never accepts `type: revoke` on this
  domain; like ct-cli, the provider removes a row with a DELETE naming `authId`,
  `isInherited` and the exact `dataId`, and it never writes a deny row. Destroy
  un-manages.
- `churchtools_person_status_grants` (`/permissions/status/{statusId}`, import id =
  status id) has no `applies_to_subgroups` and only takes authIds below 10000.
  Destroy un-manages.

- **System baseline rows** (`meta.modifiedPid` -1; prod has 97 on `group_type_role`
  domains, dev holds 70 of them as ordinary rows) follow one rule in the shared
  grants engine, for `group_role`, `group_type_role` and `person_status_grants`: a
  declared row with the same identity (authId, dataId, isInherited) is *satisfied* by
  a live system row, so there is no write and no diff, and read puts it into state as
  declared. An undeclared system row is ignored and never revoked. A declared row that
  is absent is written (PUT). The provider never revokes a system row: dropping a
  declared one from config removes it from state only, with a warning. Import reads
  without the config, so undeclared system rows stay out of state (otherwise the next
  plan would revoke them); a block declaring one afterwards shows once as an addition
  and is adopted without a write. Mock-verified only; the mock serves `modifiedPid` -1.

The mock models these domains from CT 3.137's OpenAPI text and from ct-cli's
behaviour. On 2026-10-20 all three ran live on eqrm-dev from this provider:

- `group_role_definition`: create (role 295 in group type 57), rename through
  `PUT /group/roles/{id}` with an empty follow-up plan and the unmanaged
  `shorty`/`sort_key` kept, and a destroy that deleted the role (before the 2026-10-20 change to un-manage).
- `group_type_role`: one grant written on the new role, then removed through the
  DELETE path; CT's row list was empty afterwards.
- `person_status_grants`: import of status 0 (`Unbekannt`) put only the one
  hand-written row into state and left CT's baseline rows out; a `churchdb:view`
  grant was added and removed again, and the status ended with its original rows.

Still **not verified live**:

- `group_type_role`: whether dev accepts `isInherited` on write, and data-scoped
  grants (the live run used a right without scope).
- `group_role_definition`: the shape of the 409 report when memberships still
  reference the role.
- `person_status_grants`: data-scoped grants on the `status` domain.

Run a read-only import and an empty plan on dev first.

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

## Rate limiting

ChurchTools answers too many requests with HTTP 429 and publishes no number for
its limit. Terraform applies resources in parallel (default `-parallelism=10`)
and a resource such as `churchtools_group_type_role` issues one request per
grant row, so an unpaced apply bursts hundreds of calls (eqrm/ct-structure run
39580441996 hit 429 on 12 roles that way). The provider therefore:

- **Spaces requests.** All resources share one client, which starts at most
  `max_requests_per_second` requests per second (default `10`, the value the
  sibling ct-cli settled on). `0` switches the spacing off. The environment
  variable `CHURCHTOOLS_MAX_REQUESTS_PER_SECOND` sets the same when the attribute
  is absent; the attribute wins.
- **Retries 429.** Up to 6 retries, waiting the `Retry-After` header when sent,
  else 0.5s doubling (plus jitter); each wait is capped at 60s and honours
  context cancellation. A 429 also pauses *every* request of the client for that
  wait, so parallel resources do not each retry into the wall. After the last
  retry the error says so. A 429 is retried for every method, creates included,
  because ChurchTools rejects the request before processing it. 5xx and network
  errors are never retried: a write may already have been applied.

```hcl
provider "churchtools" {
  host                    = "https://example.church.tools"
  max_requests_per_second = 5 # optional
}
```

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
