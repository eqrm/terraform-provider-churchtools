package provider

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/eqrm/terraform-provider-churchtools/internal/client"
)

// Ein Gruppentyp-Wechsel ist bei ChurchTools eine Migration, kein Feld-Update:
// PATCH /groups/{id} lehnt groupTypeId mit `400 groupTypeId: validation.always.invalid`
// ab. Der echte Weg ist
//
//	POST /groups/{id}/grouptype  {"groupTypeId": <neu>, "roleMapping": {"<alt>": <neu>}}
//
// (204, leerer Body). Schlüssel und Werte sind groupTypeRoleIds aus /group/roles, und
// das Mapping muss JEDE Rolle des bisherigen Typs enthalten. Es entscheidet, wo die
// vorhandenen Mitgliedschaften landen. Vorlage ist ct-cli (src/engine/grouptype.ts), das
// den Ablauf live gegen CT 3.137 geprüft hat.
//
// Abgeleitet wird nur, wo niemand seine Rolle verlieren kann:
//  1. gleichnamige Rolle im Zieltyp (Name als Slug verglichen, genau eine), sonst
//  2. die alte Rolle hat in dieser Gruppe KEINE Mitglieder: Standardrolle des Zieltyps
//     (isDefault, sonst eine Rolle gleicher Art, sonst die erste).
//
// Eine Rolle MIT Mitgliedern und ohne gleichnamiges Ziel wird nicht geraten, sondern
// gemeldet.

// typeRole is a /group/roles row reduced to what the mapping needs.
type typeRole struct {
	id        int64
	name      string
	typeID    int64
	kind      string // "leader" | "participant"
	isDefault bool
}

type roleMove struct {
	from, to typeRole
}

// blockedRole is a role that holds members but has no same-named target.
type blockedRole struct {
	role    typeRole
	members int
}

func roleSlug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func typeRoleFromRow(row client.Row) typeRole {
	return typeRole{
		id:        intField(row, "id"),
		name:      stringField(row, "name"),
		typeID:    intField(row, "groupTypeId"),
		kind:      stringField(row, "type"),
		isDefault: boolField(row, "isDefault"),
	}
}

func defaultTargetRole(targets []typeRole, kind string) (typeRole, bool) {
	for _, t := range targets {
		if t.isDefault {
			return t, true
		}
	}
	for _, t := range targets {
		if kind != "" && t.kind == kind {
			return t, true
		}
	}
	if len(targets) > 0 {
		return targets[0], true
	}
	return typeRole{}, false
}

// deriveRoleMapping builds the mapping for every role of the current type, or
// returns the roles that would need a guess. Deterministic and free of I/O.
func deriveRoleMapping(source, targets []typeRole, members map[int64]int) ([]roleMove, []blockedRole) {
	bySlug := map[string][]typeRole{}
	for _, t := range targets {
		k := roleSlug(t.name)
		bySlug[k] = append(bySlug[k], t)
	}
	var moves []roleMove
	var blocked []blockedRole
	for _, s := range source {
		// Two target roles with one name would make "the" same-named role a guess.
		if hit := bySlug[roleSlug(s.name)]; len(hit) == 1 {
			moves = append(moves, roleMove{s, hit[0]})
			continue
		}
		if members[s.id] == 0 {
			if d, ok := defaultTargetRole(targets, s.kind); ok {
				moves = append(moves, roleMove{s, d})
				continue
			}
		}
		blocked = append(blocked, blockedRole{s, members[s.id]})
	}
	return moves, blocked
}

// groupTypeMigration is the resolved migration of one group.
type groupTypeMigration struct {
	toTypeID int64
	moves    []roleMove
}

// body renders the POST /groups/{id}/grouptype payload.
func (m groupTypeMigration) body() client.Row {
	mapping := map[string]any{}
	for _, mv := range m.moves {
		mapping[strconv.FormatInt(mv.from.id, 10)] = mv.to.id
	}
	return client.Row{"groupTypeId": m.toTypeID, "roleMapping": mapping}
}

// planGroupTypeMigration reads the roles of both types and the group's members
// (every page: a role whose holders all sit past page 1 would count as empty and be
// mapped away without asking) and derives the mapping. The error is German and
// names every role that blocks the change.
func planGroupTypeMigration(ctx context.Context, c *client.Client, groupID string, fromType, toType int64) (groupTypeMigration, error) {
	rows, err := c.List(ctx, "/group/roles")
	if err != nil {
		return groupTypeMigration{}, fmt.Errorf("Gruppenrollen konnten nicht gelesen werden: %w", err)
	}
	var source, targets []typeRole
	for _, row := range rows {
		r := typeRoleFromRow(row)
		switch r.typeID {
		case fromType:
			source = append(source, r)
		case toType:
			targets = append(targets, r)
		}
	}
	memberRows, err := c.List(ctx, groupCollection+"/"+groupID+"/members")
	if err != nil {
		return groupTypeMigration{}, fmt.Errorf("Mitglieder der Gruppe %s konnten nicht gelesen werden: %w", groupID, err)
	}
	members := map[int64]int{}
	for _, m := range memberRows {
		if id, ok := m["groupTypeRoleId"].(float64); ok {
			members[int64(id)]++
		}
	}
	// A member holding a role the catalog does not list for the current type would
	// fall outside the mapping unnoticed.
	known := map[int64]bool{}
	for _, s := range source {
		known[s.id] = true
	}
	var orphaned []string
	for id := range members {
		if !known[id] {
			orphaned = append(orphaned, strconv.FormatInt(id, 10))
		}
	}
	if len(orphaned) > 0 {
		sort.Strings(orphaned)
		return groupTypeMigration{}, fmt.Errorf("Mitglieder der Gruppe %s haben die Rollen-id(s) %s, die /group/roles "+
			"nicht unter dem bisherigen Gruppentyp %d führt. Ohne Zuordnung wird nicht migriert.",
			groupID, strings.Join(orphaned, ", "), fromType)
	}

	moves, blocked := deriveRoleMapping(source, targets, members)
	if len(blocked) > 0 {
		var lines []string
		for _, b := range blocked {
			lines = append(lines, fmt.Sprintf("  - %q (Rolle %d, %d Mitglied(er)) hat im Gruppentyp %d keine gleichnamige Rolle",
				b.role.name, b.role.id, b.members, toType))
		}
		var names []string
		for _, t := range targets {
			names = append(names, t.name)
		}
		return groupTypeMigration{}, fmt.Errorf("Der Gruppentyp von Gruppe %s kann nicht von %d auf %d wechseln, "+
			"ohne dass Mitglieder ihre Rolle verlieren. ChurchTools ordnet beim Wechsel jede Rolle des alten Typs "+
			"einer Rolle des neuen zu, und dieses Mapping entscheidet, wo die Mitgliedschaften landen; der Provider "+
			"rät es nicht.\n%s\nDer Zieltyp %d kennt: %s.\nBenenne die Rollen im neuen Gruppentyp gleich, oder verschiebe "+
			"die Mitglieder vorher in ChurchTools in eine Rolle, die es im Zieltyp gibt.",
			groupID, fromType, toType, strings.Join(lines, "\n"), toType, strings.Join(names, ", "))
	}
	return groupTypeMigration{toTypeID: toType, moves: moves}, nil
}
