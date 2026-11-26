package queue

import (
	"fmt"
	"testing"
)

// Kept independent of handler construction so the deletion oracle's negative
// control can execute before a Queue runtime exists. A DELETE returning 204
// without removing the visible message produces a nonzero observed count.
func requireNoMessagesAfterDelete(count int) error {
	if count != 0 {
		return fmt.Errorf("DeleteMessage left %d visible messages before ClearMessages", count)
	}
	return nil
}

func TestAzureQueueDeleteOracleRejectsNoOp(t *testing.T) {
	if err := requireNoMessagesAfterDelete(0); err != nil {
		t.Fatal(err)
	}
	// Negative control: the visible pre-delete message survived a no-op DELETE.
	if err := requireNoMessagesAfterDelete(1); err == nil {
		t.Fatal("oracle accepted a successful-looking no-op DELETE")
	}
}
