# The rights a person status carries (CT: /permissions/status/<status id>).
# Status 0 is "Unbekannt". The one right ct-cli declares here is the SSO login:
# remove it by mistake and nobody can use the external login.
resource "churchtools_person_status_grants" "unbekannt" {
  person_status_id = "0"

  grant {
    right    = "churchcore:login to external system"
    data_ids = ["-1"] # oauth_client: all
  }
}

# No applies_to_subgroups here (CT has no inheritance on this domain), and only
# rights with an authId below 10000.

# Import: <status id>[/<preserve dimension>,...]
# import {
#   to = churchtools_person_status_grants.unbekannt
#   id = "0"
# }
