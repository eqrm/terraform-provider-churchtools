# terraform-provider-churchtools

An OpenTofu/Terraform provider for [ChurchTools](https://church.tools) structure:
campuses, group types, departments, person statuses, groups and permissions.

It manages the *scaffold* only. People, memberships and participants are out of
scope and are never read or written.

Resource type names are bare (`campus`, not `churchtools_campus`), because a
ChurchTools structure config is 100% this provider and the prefix is noise.

```hcl
campus "mainz" {
  name   = "Mainz"
  shorty = "MZ"
}
```

## Status

Pre-release. Tier-0 master data only: campus, group type, department, person
status, comment viewer, contact label, relationship type and privacy-policy
agreement type.

Nothing is ever deleted: every resource's `Delete` refuses and directs the
operator to the ChurchTools UI instead.

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
exercised (nothing needed one) and a **delete** never will be, since Delete
refuses by design.

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

Pass `--env`. `data "external"` requires every value in the JSON object to be a
string, and `ct auth token` reports `environment: null` when no environment was
selected — which fails inside the external provider with a message about JSON
types rather than anything naming the cause.

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

## Licence

[MPL-2.0](LICENSE) — the licence OpenTofu itself and every HashiCorp provider
use.

