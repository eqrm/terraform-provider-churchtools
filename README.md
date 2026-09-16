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
