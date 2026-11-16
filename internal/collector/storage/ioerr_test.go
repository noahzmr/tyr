package storage

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/noahzmr/tyr/internal/collector"
	"github.com/noahzmr/tyr/internal/execx"
	"github.com/noahzmr/tyr/internal/hostfs"
	"github.com/noahzmr/tyr/internal/model"
)

// rejectingSMART is smartctl behind a controller that rejects some of its
// commands: every run adds 2 to the disk's ioerr_cnt.
type rejectingSMART struct {
	errFile string
	runs    int
}

func (r *rejectingSMART) LookPath(name string) (string, error) {
	if name == "smartctl" {
		return "/usr/sbin/smartctl", nil
	}
	return "", execx.ErrNotFound
}

func (r *rejectingSMART) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	if name != "smartctl" {
		return nil, execx.ErrNotFound
	}
	r.runs++
	addErrs(r.errFile, 2)
	return []byte(`{"smartctl":{"exit_status":0},"device":{"name":"/dev/sda","protocol":"ATA"},"model_name":"VK001920GXAWN","serial_number":"S1",` +
		`"smart_status":{"passed":true},"ata_smart_attributes":{"table":[{"id":194,"name":"Temperature_Celsius","value":79,"worst":58,"thresh":0,"raw":{"value":180389412885,"string":"21 (Min/Max 18/40)"}}]}}`), nil
}

func addErrs(file string, n uint64) {
	b, _ := os.ReadFile(file)
	v, _ := strconv.ParseUint(strings.TrimPrefix(strings.TrimSpace(string(b)), "0x"), 16, 64)
	_ = os.WriteFile(file, []byte("0x"+strconv.FormatUint(v+n, 16)+"\n"), 0o644)
}

// sasDisk builds /sys for one SAS disk with ioerr_cnt = 1092.
func sasDisk(t *testing.T) (string, string) {
	root := t.TempDir()
	dev := filepath.Join(root, "sys/devices/pci0000:00/0000:00:01.0/host0/port-0:0/end_device-0:0/target0:0:0/0:0:0:0")
	for _, p := range []string{dev + "/block/sda/queue"} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{dev + "/ioerr_cnt": "0x444\n", dev + "/model": "VK001920GXAWN\n", dev + "/vendor": "ATA\n",
		dev + "/block/sda/size": "3750748848\n", dev + "/block/sda/queue/rotational": "0\n"}
	for p, v := range files {
		if err := os.WriteFile(p, []byte(v), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "sys/block"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../devices/pci0000:00/0000:00:01.0/host0/port-0:0/end_device-0:0/target0:0:0/0:0:0:0/block/sda", filepath.Join(root, "sys/block/sda")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../../0:0:0:0", dev+"/block/sda/device"); err != nil {
		t.Fatal(err)
	}
	return root, dev + "/ioerr_cnt"
}

func TestOwnSMARTQueriesAreNotDiskErrors(t *testing.T) {
	root, errFile := sasDisk(t)
	runner := &rejectingSMART{errFile: errFile}
	now := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	env := &collector.Env{FS: hostfs.New(root), Exec: runner, Now: func() time.Time { return now }, Opts: collector.DefaultOptions()}
	c := New()
	run := func(ctx context.Context) *model.Disk {
		sec, err := c.Collect(ctx, env, nil)
		if err != nil {
			t.Fatal(err)
		}
		st := sec.(*model.Storage)
		return &st.Disks[0]
	}
	// 1st collection: SMART read, 2 errors caused by it, none new
	d := run(context.Background())
	if runner.runs != 1 || *d.IOErrors != 1094 || *d.IOErrorsTyr != 2 || *d.IOErrorsNew != 0 || d.Status != model.StatusOK {
		t.Fatalf("first: runs %d errors %d tyr %d new %d status %s", runner.runs, *d.IOErrors, *d.IOErrorsTyr, *d.IOErrorsNew, d.Status)
	}
	if a := d.SMART.Attributes[0]; a.RawString != "21 (Min/Max 18/40)" {
		t.Errorf("raw string %q", a.RawString)
	}
	// within the interval SMART is reused: no query, no new errors
	now = now.Add(time.Minute)
	d = run(context.Background())
	if runner.runs != 1 || *d.IOErrorsTyr != 2 || *d.IOErrorsNew != 0 || d.SMART == nil || !d.SMARTAt.Equal(now.Add(-time.Minute)) {
		t.Fatalf("cached: runs %d tyr %d new %d", runner.runs, *d.IOErrorsTyr, *d.IOErrorsNew)
	}
	// a fresh read on request (stress snapshots, r in the TUI)
	d = run(collector.WithSMARTMaxAge(context.Background(), 0))
	if runner.runs != 2 || *d.IOErrorsTyr != 4 || *d.IOErrorsNew != 0 {
		t.Fatalf("forced: runs %d tyr %d new %d", runner.runs, *d.IOErrorsTyr, *d.IOErrorsNew)
	}
	// errors Tyr did not cause: a WARN finding, and the stress counter grows
	addErrs(errFile, 3)
	sec, _ := c.Collect(context.Background(), env, nil)
	st := sec.(*model.Storage)
	d = &st.Disks[0]
	if *d.IOErrorsNew != 3 || d.Status != model.StatusWarn || len(st.Findings) != 1 ||
		!strings.Contains(st.Findings[0].Message, "3 new SCSI command errors since Tyr started") {
		t.Fatalf("new errors: new %d status %s findings %+v", *d.IOErrorsNew, d.Status, st.Findings)
	}
	if cnt := (&model.Snapshot{Storage: st}).Counters(); cnt["disk.sda.io_errors"] != 1092+3 {
		t.Errorf("counter without Tyr's own errors: %d", cnt["disk.sda.io_errors"])
	}
}
