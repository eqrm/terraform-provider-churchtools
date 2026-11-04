resource "churchtools_grow_path" "friend" {
  name     = "Friend"
  color    = "sky"
  sort_key = 1 # the stage with the higher sort_key wins among a person's roles
}

# Map a role to the stage with grow_path_id on churchtools_group_role_definition:
#   grow_path_id = tonumber(churchtools_grow_path.friend.id)
