package gpu

import "testing"

func TestParseXRTSMI(t *testing.T) {
	idle := `
------------------------------
[0000:c5:00.1] : RyzenAI-npu6
------------------------------
AIE Partitions
  No hardware contexts running on device

Platform
  Name                   : RyzenAI-npu6 
  Power Mode             : Default 
  Total Columns          : 8 

Estimated Power          : 0.002 Watts
`
	x := ParseXRTSMI([]byte(idle))
	if x.PowerMode != "Default" || x.Columns == nil || *x.Columns != 8 || x.PowerWatts == nil || *x.PowerWatts != 0.002 ||
		x.Contexts == nil || *x.Contexts != 0 {
		t.Fatalf("idle: %+v", x)
	}
	busy := `AIE Partitions
  Partition Index: 0
    Columns: [0, 1, 2, 3]
    HW Contexts:
      |PID    |Ctx ID  |Submissions |Migrations |Err |Priority |
      |-------|--------|------------|-----------|----|---------|
      |4711   |1       |1200        |0          |0   |Normal   |
      |4712   |2       |33          |0          |0   |Normal   |

Platform
  Power Mode             : Performance
Estimated Power          : N/A
`
	x = ParseXRTSMI([]byte(busy))
	if x.PowerWatts != nil || x.PowerMode != "Performance" || x.Contexts == nil || *x.Contexts != 2 {
		t.Fatalf("busy: %+v", x)
	}
}
