# 3. Context and scope

## Business context

```mermaid
flowchart LR
    admin["Admin / CI"] -->|"tofu plan / apply / import"| tofu["OpenTofu"]
    exporter["ct-cli exporter<br/>(ct export tf)"] -->|"HCL + import blocks"| cfg["ct-structure<br/>(HCL config, consumer)"]
    cfg --> tofu
    ctcli["ct auth token"] -->|"session cookie + CSRF"| tofu
    tofu <-->|"plugin protocol v6"| prov["terraform-provider-churchtools"]
    prov -->|"REST /api"| ct["ChurchTools instance"]
    prov -->|"legacy /index.php?q=churchdb/ajax"| ct
    reg["OpenTofu registry"] -->|"tofu init: signed binary"| tofu
    gh["GitHub release (git tag)"] --> reg
```

| Partner | Exchange |
|---|---|
| ChurchTools | The system under management (REST and legacy endpoint). |
| OpenTofu / Terraform | Hosts the provider as a plugin process. |
| `ct-cli` | `ct export tf` renders HCL and `import` blocks from its state; `ct auth token` supplies a session. |
| `ct-structure` | Private consumer: the HCL configuration, CI plan/apply, `prevent_destroy` on every generated resource. |
| OpenTofu registry | Distribution; serves the assets of a GitHub release verbatim. |

## Technical context

| Channel | Protocol | Notes |
|---|---|---|
| Provider to ChurchTools REST | HTTPS JSON, `/api/...`, `{"data": ...}` envelope | Token: `Authorization: Login <token>`. Session: `Cookie` plus `CSRF-Token` on writes. |
| Provider to legacy endpoint | HTTPS, form-encoded POST to `/index.php?q=churchdb/ajax` | Needs cookie plus CSRF token; failure is HTTP 200 with `{"status":"error"}`. |
| OpenTofu to provider | gRPC plugin protocol v6 | Local process. |

Out of scope: people, memberships, participants (never touched).
