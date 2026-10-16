# 6. Runtime view

## REST resource (e.g. campus)

```mermaid
sequenceDiagram
    participant T as OpenTofu
    participant R as Resource
    participant C as client
    participant CT as ChurchTools
    T->>R: Create(plan)
    R->>C: Create("/campuses", body)
    C->>CT: POST /api/campuses
    CT-->>C: {"data": {id, ...}}
    R->>R: requireID(row) fails on missing id
    R-->>T: state with id
    T->>R: Read(state)
    R->>C: Get("/campuses", id)
    C->>CT: GET /api/campuses/{id}
    Note over C: 404 on an item becomes ErrNotFound, so the resource leaves state
    T->>R: Update(plan, state)
    R->>C: Update(..., "PUT", body)
    Note over R: an empty 204 body is no error
```

An empty id is refused (`ErrNoID`) because `GET /campuses/` would address the
whole collection. A 404 on a *collection* path is a plain error, never
`ErrNotFound`: it means the endpoint doesn't exist.

## Legacy master-data resource (department)

There is no `GET /departments/{id}`, and the write returns no id.

```mermaid
sequenceDiagram
    participant R as department
    participant C as client
    participant CT as ChurchTools
    R->>C: LockMasterDataCreate()
    R->>C: List("/departments") -> before
    Note over R: refuse if a department of the same name exists
    R->>C: SaveMasterData("cdb_bereich", fields, nil)
    C->>CT: getMasterData (columns of this instance)
    C->>CT: session: whoami, csrftoken (once per client)
    C->>CT: POST /index.php?q=churchdb/ajax saveMasterData
    R->>C: List("/departments") -> after
    Note over R: exactly one new id = the created row, otherwise error
    R-->>R: state.id = new id
```

The lock exists because Terraform creates independent resources in parallel and
two interleaved snapshot-diff sequences could not claim their rows. Update is the
same `saveMasterData` call with the row id set; Read filters the list.

## Import and no-op plan

1. `import { to = churchtools_x.name, id = "<numeric id>" }`; `ImportState` passes the id through.
2. Read fills every attribute from ChurchTools.
3. The configuration is compared with that state; an empty plan is the acceptance criterion.

`TestGolden_*` in `internal/provider/golden_test.go` automates this: it seeds
the mock from a synthetic ct-cli state file, generates HCL the way the exporter
does, imports every row and asserts an empty plan, including a row with id 0.
Its attribute mapping must stay identical to the exporter's.

## Delete

`Delete` adds the warning "Objekt bleibt in ChurchTools bestehen" and returns;
no request is sent.
