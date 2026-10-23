# The rule of one group. The ruleset stays a JSON file (copy-paste from ChurchTools or
# ct-cli's rulesets/), with `__ctRef` markers where a host-specific id belongs.
# `refs` names an id for every marker; pointing it at other resources puts the rule
# after the groups, campuses and group types it mentions.
resource "churchtools_dynamic_group" "kids_auto" {
  group_id = churchtools_group.kids_auto.id
  status   = "active" # or "manual"; "none" is not managed here
  ruleset  = file("${path.module}/rulesets/kids_auto.json")

  refs = {
    "group-type:group"             = churchtools_group_type.group.id
    "group:kids"                   = churchtools_group.kids.id
    "campus:mainz"                 = churchtools_campus.mainz.id
    "group-type-role:group/leiter" = churchtools_group_role_definition.group_leiter.id
  }
}

# Adopt a rule that already exists (import id = group id, also for archived groups):
import {
  to = churchtools_dynamic_group.kids_auto
  id = "123"
}
