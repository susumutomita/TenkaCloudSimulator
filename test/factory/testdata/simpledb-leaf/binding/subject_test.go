// This package is compiled only after the independent runner verifies production
// implementation presence. A compile failure is BINDING_UNESTABLISHED, never RED.
package binding_test

import (
	"database/sql"
	leaf "github.com/susumutomita/TenkaCloudSimulator/internal/aws/simpledb"
	oracle "github.com/susumutomita/TenkaCloudSimulator/test/factory/testdata/simpledb-leaf/contract"
	"testing"
)

func TestSimpleDBLeafProductionContract(t *testing.T) {
	r := oracle.Run(t.TempDir(), func(db *sql.DB) (oracle.Store, error) { return leaf.NewStore(db) }, oracle.Required)
	for _, c := range r.Cases {
		t.Run(c.ID, func(t *testing.T) {
			if c.Status != "PASS" {
				t.Fatalf("%s: %s", c.Status, c.Reason)
			}
		})
	}
	t.Log("LEAF_CONTRACT_REPORT " + oracle.Encode(r))
}
