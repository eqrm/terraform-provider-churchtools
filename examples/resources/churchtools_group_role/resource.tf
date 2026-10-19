# The Mitglied role of one group: what its members may do.
resource "churchtools_group_role" "sll_koblenz_mitglied" {
  group_id = churchtools_group.sll_koblenz.id
  role     = "Mitglied" # CT's `name`, not the translated label

  # Grants on these dimensions that are not declared below are left alone
  # (reports and templates are handed out by hand in the UI). Every other
  # undeclared grant is planned for revocation.
  preserve_unknown = ["crp_query", "cc_html_template"]

  grant {
    right       = "churchdb:view alldata"
    departments = ["2"]
  }
  grant {
    right    = "churchdb:view station"
    campuses = ["24"]
  }
  # The same right again, but for the group's SUBGROUPS (CT: isInherited).
  grant {
    right                = "churchdb:+see persons"
    applies_to_subgroups = true
    security_levels      = ["1", "2", "3"]
  }
  # An unscoped right: no selection argument.
  grant {
    right = "churchdb:view"
  }
}

# Import: <group id>/<role name>[/<preserve dimension>,...]
# import {
#   to = churchtools_group_role.sll_koblenz_mitglied
#   id = "1748/Mitglied/crp_query,cc_html_template"
# }
