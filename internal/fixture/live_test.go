package fixture

import (
	"context"
	"testing"

	"github.com/noahzmr/tyr/internal/collector"
	"github.com/noahzmr/tyr/internal/collector/cpu"
)

func TestEnableLiveMovesUtilization(t *testing.T) {
	env, _, err := Load("../../testdata/fixtures/server-healthy", collector.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	EnableLive(env)
	snap := collector.Collect(context.Background(), env, []collector.Collector{cpu.New()}, nil)
	busy, set := 0, 0
	for _, l := range snap.CPU.Logical {
		if l.UtilizationPercent != nil {
			set++
			if *l.UtilizationPercent > 50 {
				busy++
			}
		}
	}
	if set != 64 || busy == 0 {
		t.Errorf("utilization set on %d CPUs, %d busy", set, busy)
	}
	if !env.LiveSim {
		t.Error("LiveSim not set")
	}
}

func TestLoadIsMonotonic(t *testing.T) {
	prev := 0.0
	for x := 0.0; x < 30; x += 0.7 {
		v := busyIntegral(5, x)
		if v < prev || v > x {
			t.Fatalf("busyIntegral(5,%v)=%v prev %v", x, v, prev)
		}
		prev = v
	}
}
