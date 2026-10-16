# 8. Crosscutting concepts

## Ids

- Terraform ids are strings. ChurchTools ids decode as `float64` from JSON; `idString` formats them.
- **Id 0 is valid** (one campus has it). No code may treat 0 or `"0"` as absent; golden and campus tests cover it.
- `requireID` turns a missing id in a create response into an error. An empty string would be accepted as a known computed value and make the next read address the collection.
- The legacy endpoint encodes values as strings (`"sortkey": "20"`); resources parse what they need.

## Language

Code, attribute names and comments are English. Everything an operator reads
(plan warnings, errors) is German, e.g. "Provider nicht konfiguriert",
"Objekt bleibt in ChurchTools bestehen". Reason: the rights vocabulary comes from
the ChurchTools UI. Shared German texts live in `errors.go`.

## Authentication

Exactly one mode; supplying both, or half a session, is a configure-time error.

| Mode | Attributes | Behaviour |
|---|---|---|
| Session (preferred) | `session_cookie` + `csrf_token` | Cookie on every call, CSRF header on writes. A 401 yields `ErrSessionExpired` telling the operator to re-run; the provider can't renew a session it was handed. |
| Login token | `token` | `Authorization: Login <token>` for REST. For the legacy endpoint the client buys a session lazily (whoami with `login_token`, then `/csrftoken`), once per client, under a mutex. |

If any credential is unknown at plan time (normal for a session from a
`data "external"` block), `Configure` returns early without data. Resources then
hold a nil client; every CRUD body guards with `notConfigured`, so the result is
a German diagnostic rather than a plugin crash. All credentials are `Sensitive`.

## Pagination

`List` follows `meta.pagination` (page size 100, hard stop at 500 pages). Reading
only page 1 would silently truncate, and departments find their id by filtering
the list, so a short read would defeat the duplicate-name guard.

## Concurrency

One shared client; session fields are mutex-guarded; legacy creates are
serialised by `LockMasterDataCreate`.

## Mock fidelity rules

From `CLAUDE.md`. The mock is not ChurchTools.

1. A new collection must be registered in `supportedVerbs` with the verbs a live instance serves; an unregistered one answers 404, an unsupported verb 405.
2. Collections whose PUT answers 204 without a body go in `putNoContent`.
3. Collections where PUT replaces (no PATCH) list their mandatory keys in `putRequired`.
4. When a change depends on a live answer (status, empty body, ids, verbs), check the OpenAPI spec or probe a development instance and fix the mock in the same PR.
5. Update the README's "What is verified" section when a path has run live.

## Legacy write safety

Tables are an allowlist (`writableTables`); columns are validated against the
instance's own `getMasterData` description before posting, because the endpoint
accepts unknown columns silently.
