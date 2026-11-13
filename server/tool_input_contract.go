package server

import (
	"encoding/json"
	"log"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

// Update only the schema on existing built-ins, preserving descriptions,
// bindings, enabled/deferred flags and parameter annotations. JSONB equality
// makes this idempotent; compare-and-swap avoids overwriting concurrent edits.
func repairBuiltinToolContracts(pg *db.DB) {
	for _, key := range []string{"record_fact", "list_task_credentials"} {
		row, err := pg.GetTool(key)
		if err != nil {
			log.Printf("[tools] read %s contract: %v", key, err)
			continue
		}
		if row == nil || !row.System || row.Kind != "builtin" {
			continue
		}
		var schema map[string]any
		if err := json.Unmarshal(row.Schema, &schema); err != nil {
			log.Printf("[tools] decode %s contract: %v", key, err)
			continue
		}
		raw, err := json.Marshal(agent.RepairBuiltinToolSchema(key, schema))
		if err != nil {
			log.Printf("[tools] encode %s contract: %v", key, err)
			continue
		}
		if _, err := pg.Exec(`UPDATE tools SET schema=$2::jsonb, updated_at=now()
WHERE key=$1 AND system AND kind='builtin' AND schema=$3::jsonb AND schema IS DISTINCT FROM $2::jsonb`, key, string(raw), string(row.Schema)); err != nil {
			log.Printf("[tools] repair %s contract: %v", key, err)
		}
	}
}
