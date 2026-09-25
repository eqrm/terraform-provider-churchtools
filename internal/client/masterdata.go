package client

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
)

// The legacy master-data registry (ct-cli #108/#109).
//
//	func=getMasterData                                       -> data.masterDataTables
//	func=saveMasterData&table=…&id=&col0=…&value0=…           -> empty id creates, a set id updates
//
// SCOPE: two tables. `cdb_bereich` (Bereiche/departments) was the first: a live
// classification of all 24 tables found 15 with a REST write path — those stay
// REST — and of the 9 without, Bereiche were the only one inside this tool's
// structural mandate at the time.
//
// `cdb_privacy_policy_agreement_types` joined in ct-structure#121 (Felix,
// 2026-09-25: "everything that is defined needs to be in ct-structure"). It has
// no REST endpoint at all; its write is the same call the ChurchTools admin UI
// makes (Stammdaten → Datenschutz-Zustimmungsarten): cc_maintainstandardview.js
// → renderEditEntry posts {func:"saveMasterData", table:<tablename>, id,
// col0/value0…} via churchInterface.jsendWrite to churchdb/ajax.
//
// Widening this further needs the same live re-probe ct-cli's
// runbook-manual-surface.md describes; the endpoint is undocumented.
const (
	MasterDataModule           = "churchdb"
	DepartmentTable            = "cdb_bereich"
	PrivacyAgreementTypesTable = "cdb_privacy_policy_agreement_types"
)

// writableTables is an allowlist, not a convenience. The legacy endpoint will
// happily write any table it knows, including person master data this tool has
// no mandate over.
var writableTables = map[string]bool{DepartmentTable: true, PrivacyAgreementTypesTable: true}

type masterDataColumn struct {
	Field string `json:"field"`
}

type masterDataTable struct {
	ID        int                         `json:"id"`
	TableName string                      `json:"tablename"`
	Desc      map[string]masterDataColumn `json:"desc"`
}

type masterDataEnvelope struct {
	MasterDataTables map[string]masterDataTable `json:"masterDataTables"`
}

// masterDataTable fetches the instance's SELF-DESCRIBING table registry, which
// is what makes the column check below real: the columns are the instance's
// own, not a list hard-coded here that a ChurchTools rename would silently
// invalidate.
func (c *Client) masterDataTable(ctx context.Context, tablename string) (masterDataTable, error) {
	if !writableTables[tablename] {
		return masterDataTable{}, fmt.Errorf(
			"churchtools: refusing to write master-data table %q — this provider drives only %q and %q",
			tablename, DepartmentTable, PrivacyAgreementTypesTable)
	}
	var env masterDataEnvelope
	if err := c.AjaxJSON(ctx, MasterDataModule, map[string]string{"func": "getMasterData"}, &env); err != nil {
		return masterDataTable{}, err
	}
	for _, t := range env.MasterDataTables {
		if t.TableName == tablename {
			return t, nil
		}
	}
	return masterDataTable{}, fmt.Errorf("churchtools: this instance has no master-data table %q", tablename)
}

// SaveMasterData creates (empty id) or updates (set id) one row.
//
// Columns are validated against the instance's own DESCRIBE before anything is
// sent: the legacy endpoint accepts an unknown column SILENTLY, so an
// unvalidated write after a ChurchTools rename would look like a success and
// change nothing.
func (c *Client) SaveMasterData(ctx context.Context, tablename string, fields map[string]any, id *int) error {
	table, err := c.masterDataTable(ctx, tablename)
	if err != nil {
		return err
	}

	params := map[string]string{"func": "saveMasterData", "table": tablename, "id": ""}
	if id != nil {
		params["id"] = strconv.Itoa(*id)
	}

	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names) // deterministic colN ordering

	n := 0
	for _, name := range names {
		if name == "id" {
			continue // the row key, sent separately
		}
		value := fields[name]
		if value == nil {
			continue
		}
		if _, ok := table.Desc[name]; !ok {
			known := make([]string, 0, len(table.Desc))
			for k := range table.Desc {
				known = append(known, k)
			}
			sort.Strings(known)
			return fmt.Errorf(
				"churchtools: master-data table %q on this instance has no column %q — it has: %v. "+
					"Refusing to post an unknown column (the legacy endpoint would accept it silently)",
				tablename, name, known)
		}
		params[fmt.Sprintf("col%d", n)] = name
		params[fmt.Sprintf("value%d", n)] = fmt.Sprint(value)
		n++
	}
	if n == 0 {
		return fmt.Errorf("churchtools: nothing to write to %q: no non-empty managed columns", tablename)
	}

	return c.Ajax(ctx, MasterDataModule, params)
}

// MasterDataRows reads one row set out of getMasterData's payload, keyed by the
// row id as a string -- e.g. "privacy_policy_agreement_types", which has no REST
// endpoint at all. READ-ONLY: the write allowlist above does not apply, and
// nothing here can reach saveMasterData.
//
// The legacy payload encodes every value as a string ("sortkey": "20"); callers
// parse what they need.
func (c *Client) MasterDataRows(ctx context.Context, key string) (map[string]map[string]any, error) {
	var env map[string]json.RawMessage
	if err := c.AjaxJSON(ctx, MasterDataModule, map[string]string{"func": "getMasterData"}, &env); err != nil {
		return nil, err
	}
	raw, ok := env[key]
	if !ok {
		return nil, fmt.Errorf("churchtools: getMasterData on this instance carries no %q", key)
	}
	var rows map[string]map[string]any
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("churchtools: getMasterData %q is not an id-keyed row set: %w", key, err)
	}
	return rows, nil
}
