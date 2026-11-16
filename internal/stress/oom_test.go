package stress

import (
	"os"
	"strings"
	"testing"
)

func TestPreferOOMKill(t *testing.T) {
	restore, err := preferOOMKill()
	if err != nil {
		t.Skip(err)
	}
	b, _ := os.ReadFile("/proc/self/oom_score_adj")
	restore()
	if strings.TrimSpace(string(b)) != "1000" {
		t.Errorf("oom_score_adj %q, want 1000", b)
	}
}
