# What a role of a group TYPE grants (CT: /permissions/group_type_role/<role id>).
# This is the type-level default; churchtools_group_role holds one group's own
# grants on top of it.
resource "churchtools_group_type_role" "mentor" {
  role_id = churchtools_group_role_definition.mentor.id

  # Undeclared grants on these dimensions are left alone.
  preserve_unknown = ["crp_query"]

  grant {
    right       = "churchdb:view alldata"
    departments = ["2"]
  }
  grant {
    right                = "churchdb:+see persons"
    applies_to_subgroups = true
    security_levels      = ["1", "2", "3"]
  }
  grant {
    right = "churchdb:view"
  }
}

# Removing a grant block removes the row (DELETE with the exact dataId; CT never
# accepts a revoke row on this domain). Destroying the resource revokes nothing.

# Import: <role id>[/<preserve dimension>,...]
# import {
#   to = churchtools_group_type_role.mentor
#   id = "160/crp_query"
# }
