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

Pre-release. Tier-0 master data (campus, group type, department, person status,
comment viewer) only.

Nothing is ever deleted: every resource's `Delete` refuses and directs the
operator to the ChurchTools UI instead.

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
