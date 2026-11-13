package server

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
)

func TestRepairBuiltinToolContractsPreservesCatalogSettings(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip(err)
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	for _, key := range []string{"record_fact", "list_task_credentials"} {
		original, err := pg.GetTool(key)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if original == nil {
				_, _ = pg.Exec(`DELETE FROM tools WHERE key=$1`, key)
			} else {
				agents, _ := json.Marshal(original.Agents)
				_, _ = pg.Exec(`UPDATE tools SET description=$2,schema=$3,agents=$4,enabled=$5,deferred=$6 WHERE key=$1`, key, original.Description, original.Schema, string(agents), original.Enabled, original.Deferred)
			}
		}()
		_, err = pg.Exec(`INSERT INTO tools(key,system,description,schema,agents,enabled,kind,deferred)
VALUES($1,true,'edited description',$2,'["worker"]',false,'builtin',true)
ON CONFLICT(key) DO UPDATE SET description=EXCLUDED.description,schema=EXCLUDED.schema,agents=EXCLUDED.agents,enabled=false,deferred=true`, key,
			`{"type":"object","properties":{"summary":{"type":"string","description":"edited summary"},"confidence":{"type":"string","default":"inferred"}},"required":[""]}`)
		if err != nil {
			t.Fatal(err)
		}
	}
	repairBuiltinToolContracts(pg)
	for _, key := range []string{"record_fact", "list_task_credentials"} {
		row, err := pg.GetTool(key)
		if err != nil || row == nil {
			t.Fatal(err)
		}
		if row.Description != "edited description" || row.Enabled || !row.Deferred || len(row.Agents) != 1 || row.Agents[0] != "worker" {
			t.Fatalf("settings changed: %+v", row)
		}
		var schema map[string]any
		if err := json.Unmarshal(row.Schema, &schema); err != nil {
			t.Fatal(err)
		}
		if _, exists := schema["required"]; exists {
			t.Fatal("broken root requirement retained")
		}
		if key == "record_fact" {
			if schema["anyOf"] == nil || schema["properties"].(map[string]any)["facts"] == nil {
				t.Fatal("fact schema not upgraded")
			}
			if schema["properties"].(map[string]any)["confidence"].(map[string]any)["default"] != "inferred" {
				t.Fatal("default lost")
			}
		}
	}
	var before, after time.Time
	if err := pg.QueryRow(`SELECT updated_at FROM tools WHERE key='record_fact'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	repairBuiltinToolContracts(pg)
	if err := pg.QueryRow(`SELECT updated_at FROM tools WHERE key='record_fact'`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if !before.Equal(after) {
		t.Fatal("repeated migration changed the row")
	}
}
