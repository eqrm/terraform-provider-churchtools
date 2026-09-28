# Working in terraform-provider-churchtools

The README covers building, tests and the release prerequisites. This file adds what past sessions had to learn.

## The mock is not ChurchTools

`TF_ACC=1 go test ./...` runs against `internal/testmock/ct.go`, not against a live instance, so a green suite only proves the provider agrees with the mock. It has hidden real bugs before: it once answered every `PUT` with a body where live CT returns `204` with none (see `putNoContent`), and its verb table gates on method alone. When a change or review finding depends on what CT returns (status codes, empty bodies, ids on create, which verbs exist), check the mock handler against the OpenAPI spec or a small probe on eqrm-dev (`ct get raw`), and make the mock honest in the same PR. The README's "What is verified, and what is not" section is the record of which paths have run live, so update it when that changes.

CI runs `go build ./...`, `go vet ./...` and `TF_ACC=1 go test ./... -race`. It doesn't run `gofmt -l .`, so run that locally.

## A tag is a publication

Pushing a `vX.Y.Z` tag publishes to the OpenTofu registry, and a published version can't be taken back. Merging a PR is not a release: tag only when the user says so. `eqrm/ct-structure` is the usual consumer and often cuts the release itself, so check whether another session is already tagging before doing it here. The registry takes roughly 20–30 minutes to serve a new version.
