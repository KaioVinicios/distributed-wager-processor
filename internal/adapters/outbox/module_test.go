package outbox

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Covers: OUT-03 (spec M4, decision 14; messaging.md §5.1)
func TestInstanceID(t *testing.T) {
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	id := instanceID()
	prefix := host + "-" + strconv.Itoa(os.Getpid()) + "-"
	if !strings.HasPrefix(id, prefix) || !regexp.MustCompile(`^[0-9a-f]{8}$`).MatchString(strings.TrimPrefix(id, prefix)) {
		t.Fatalf("instanceID() = %q, want %s<8 hex>", id, prefix)
	}
	if instanceID() == id {
		t.Fatal("two identities in the same process are equal; want a random suffix")
	}
}
