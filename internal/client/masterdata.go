package client

import (
	"context"
	"fmt"
	"sort"
	"strconv"
)

// The legacy master-data registry (ct-cli #108/#109).
//
//	func=getMasterData                                       -> data.masterDataTables
//	func=saveMasterData&table=…&id=&col0=…&value0=…           -> empty id creates, a set id updates
//
// SCOPE: exactly one table, `cdb_bereich` (Bereiche/departments). A live
// classification of all 24 tables found 15 with a REST write path — those stay
// REST — and of the 9 without, Bereiche are the only one inside this tool's
// structural mandate. Widening this needs the same live re-probe ct-cli's
// runbook-manual-surface.md describes; the endpoint is undocumented.
const (
	MasterDataModule = "churchdb"
	DepartmentTable  = "cdb_bereich"
)

// writableTables is an allowlist, not a convenience. The legacy endpoint will
// happily write any table it knows, including person master data this tool has
// no mandate over.
var writableTables = map[string]bool{DepartmentTable: true}

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
			"churchtools: refusing to write master-data table %q — this provider drives only %q",
			tablename, DepartmentTable)
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
