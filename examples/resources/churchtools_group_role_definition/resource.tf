# A role of a group type (CT: /group/roles), e.g. "Mentor" for small groups.
resource "churchtools_group_role_definition" "mentor" {
  group_type_id = churchtools_group_type.kleingruppe.id
  name          = "Mentor"
  type          = "participant" # or "leader"

  # Optional. On create they default to: shorty = the first letters of the
  # name, sort_key = 0, is_default = false, is_hidden = false. Later they keep
  # whatever CT has unless you set them.
  shorty     = "Me"
  sort_key   = 5
  is_hidden  = false
  is_default = false

  # Deleting a role deletes it in ChurchTools, and CT answers 409 while any
  # membership still uses it. Guard it.
  lifecycle {
    prevent_destroy = true
  }
}

# Import: the role id.
# import {
#   to = churchtools_group_role_definition.mentor
#   id = "160"
# }
