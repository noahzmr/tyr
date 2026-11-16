package network

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/noahzmr/tyr/internal/collector"
)

// TestLiveHost collects from the real machine. Run with TYR_LIVE=1.
func TestLiveHost(t *testing.T) {
	if os.Getenv("TYR_LIVE") == "" {
		t.Skip("set TYR_LIVE=1 to collect from this host")
	}
	env := collector.NewHostEnv(collector.DefaultOptions())
	snap := collector.Collect(context.Background(), env, []collector.Collector{New()}, nil)
	b, _ := json.MarshalIndent(snap.Network, "", "  ")
	t.Logf("%s", b)
}
