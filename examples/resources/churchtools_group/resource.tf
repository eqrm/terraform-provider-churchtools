resource "churchtools_group" "ops_koblenz" {
  name            = "sKidsCheckinOpsKoblenz"
  group_type_id   = churchtools_group_type.ops.id
  group_status_id = "2"

  # Berechtigungen -> "Wie viele Ebenen von Untergruppen sollen einbezogen werden?"
  # One of 0, 1, 2, 3, 999 (999 = all levels). Left out: not managed, nothing is read.
  subgroup_permission_depth = 1
}
