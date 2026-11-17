package tui

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

	"github.com/noahzmr/tyr/internal/collector"
	"github.com/noahzmr/tyr/internal/collector/all"
	"github.com/noahzmr/tyr/internal/fixture"
	"github.com/noahzmr/tyr/internal/model"
)

// run feeds the commands a key produces back into the model (e.g. the
// switchTabMsg a finding emits on enter).
func run(m *Model, keys ...string) {
	for _, k := range keys {
		_, cmd := m.Update(press(k))
		for cmd != nil {
			msg := cmd()
			if msg == nil {
				break
			}
			if _, ok := msg.(tea.BatchMsg); ok {
				break
			}
			_, cmd = m.Update(msg)
		}
	}
}

func text(m *Model) string { return ansi.Strip(screen(m)) }

// focus selects a component in tab i (1-based key) by name.
func focus(t *testing.T, m *Model, tabKey, subject string) {
	t.Helper()
	run(m, tabKey)
	if !m.tabs[m.active].(focuser).Focus(subject) {
		t.Fatalf("no %s in %s", subject, m.tabs[m.active].Title())
	}
}

func TestFindingJumpsToComponent(t *testing.T) {
	m := newLoaded(t, "server-degraded", 120, 40)
	// down past the last card row enters the findings list; the first
	// finding is "1 core offline (cpu7)".
	run(m, "n", "enter") // n moves into Findings; the first is "1 core offline (cpu7)"
	if m.tabs[m.active].Title() != "CPU" {
		t.Fatalf("enter on finding -> %s\n%s", m.tabs[m.active].Title(), text(m))
	}
	if s := text(m); !strings.Contains(s, "cpu7 · idle states") || m.tabs[1].(*cpuTab).view != 1 {
		t.Errorf("cpu7 not selected in cores view\n%s", s)
	}
	// a storage finding selects the filesystem
	m = newLoaded(t, "server-degraded", 120, 40)
	run(m, "n")
	ov := m.tabs[0].(*overviewTab)
	for range 40 { // down to "/data 96% full"
		if it, _ := ov.finds.current(); strings.Contains(it.key, "fs_full") {
			break
		}
		run(m, "down")
	}
	run(m, "enter")
	s := text(m)
	if m.tabs[m.active].Title() != "Storage" || m.tabs[3].(*storageTab).view != 2 {
		t.Fatalf("storage finding -> %s\n%s", m.tabs[m.active].Title(), s)
	}
	run(m, "enter")
	if s := text(m); !strings.Contains(s, "Filesystems › /data") || !strings.Contains(s, "READ-ONLY") {
		t.Errorf("filesystem page\n%s", s)
	}
}

func TestProblemsOnlyAndOrder(t *testing.T) {
	m := newLoaded(t, "server-degraded", 120, 40)
	run(m, "4")
	s := text(m)
	// problems first: the first disk row is a CRIT/WARN disk, not nvme0n1
	if i, j := strings.Index(s, "✖ sdd"), strings.Index(s, "● nvme0n1"); i < 0 || j < 0 || i > j {
		t.Errorf("problems not first\n%s", s)
	}
	run(m, "!")
	s = text(m)
	if strings.Contains(s, "● nvme0n1") || !strings.Contains(s, "sdc") || !strings.Contains(s, "problems only") {
		t.Errorf("problems-only filter\n%s", s)
	}
	run(m, "!", "o")
	if s := text(m); strings.Index(s, "nvme0n1 ") > strings.Index(s, "sdd ") {
		t.Errorf("device order\n%s", s)
	}
	// "n" jumps to the next problem row in device order
	run(m, "n")
	if it, _ := m.tabs[3].(*storageTab).lists[0].current(); it.key != "sdf" { // after sdd: the iSCSI volume
		t.Errorf("next problem selected %q", it.key)
	}
}

func TestDetailPagesAndSearch(t *testing.T) {
	m := newLoaded(t, "server-degraded", 140, 50)
	focus(t, m, "4", "sdc")
	run(m, "enter")
	if m.page == nil {
		t.Fatal("no page")
	}
	s := text(m) + ansi.Strip(m.page.body)
	for _, want := range []string{"Disks › sdc", "SMART health", "pending sectors", "Reallocated_Sector_Ct", "SMART attributes", "Counters", "Findings", "8 pending sectors"} {
		if !strings.Contains(s, want) {
			t.Errorf("disk page missing %q\n%s", want, s)
		}
	}
	run(m, "/", "R", "e", "a", "l", "l", "o", "c", "enter")
	if s := text(m); !strings.Contains(s, "match 1 of") {
		t.Errorf("search\n%s", s)
	}
	run(m, "esc", "esc")
	if m.page != nil {
		t.Fatal("esc did not close the page")
	}
	checkFits(t, "after page", screen(m), 140, 50)

	// every module page renders and fits, at 80x24 too
	for _, size := range [][2]int{{80, 24}, {160, 50}} {
		for _, tab := range []string{"2", "3", "4", "6", "7", "0"} {
			m := newLoaded(t, "server-degraded", size[0], size[1])
			run(m, tab, "enter")
			if m.page == nil {
				t.Errorf("tab %s: enter opened no page", tab)
				continue
			}
			checkFits(t, "page tab "+tab, screen(m), size[0], size[1])
			run(m, "pgdown", "G")
			checkFits(t, "page end tab "+tab, screen(m), size[0], size[1])
		}
	}
	gpu := newLoaded(t, "server-degraded", 140, 60)
	run(gpu, "7", "enter")
	for _, want := range []string{"GPU", "PCIe and power state", "Reliability", "ECC uncorrectable", "row remap"} {
		if !strings.Contains(strings.ToLower(text(gpu)), strings.ToLower(want)) {
			t.Errorf("gpu page missing %q\n%s", want, text(gpu))
		}
	}
	nic := newLoaded(t, "server-degraded", 140, 60)
	focus(t, nic, "6", "ens1f0")
	run(nic, "enter")
	for _, want := range []string{"Network › ens1f0", "Transceiver", "rx power", "LLDP neighbor", "Driver error counters"} {
		if !strings.Contains(strings.ToLower(text(nic)), strings.ToLower(want)) {
			t.Errorf("nic page missing %q\n%s", want, text(nic))
		}
	}
}

func TestSessionNewResolvedRising(t *testing.T) {
	env, _, err := fixture.Load(fixtures+"server-degraded", collector.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	m := newLoaded(t, "server-degraded", 140, 60)
	snap := collector.Collect(context.Background(), env, all.Collectors(), nil)
	// second refresh: sdc gained I/O errors (new finding + rising counter),
	// the read-only /data finding is gone (resolved).
	for i := range snap.Storage.Disks {
		if snap.Storage.Disks[i].Name == "sdc" {
			n := uint64(5)
			snap.Storage.Disks[i].IOErrors = &n
			snap.Storage.Find(model.StatusWarn, "io_errors", "sdc", "smart", "sdc: 5 I/O errors (ioerr_cnt)")
		}
	}
	var keep []model.Finding
	for _, f := range snap.Storage.Findings {
		if f.Code != "fs_read_only" {
			keep = append(keep, f)
		}
	}
	snap.Storage.Findings = keep
	m.Update(snapshotMsg{snap: snap})
	s := text(m)
	for _, want := range []string{"sdc: 5 I/O errors", "NEW", "Rising counters", "disk.sdc.io_errors", "Resolved this session", "/data (xfs) is mounted read-only"} {
		if !strings.Contains(s, want) {
			t.Errorf("overview missing %q\n%s", want, s)
		}
	}
	focus(t, m, "4", "sdc")
	run(m, "enter")
	if s := text(m); !strings.Contains(s, "+5 this session") {
		t.Errorf("disk page counters\n%s", s)
	}
}

func TestTreeSlotMapHelpMouse(t *testing.T) {
	m := newLoaded(t, "server-degraded", 140, 60)
	run(m, "0", "v")
	s := text(m)
	inv := m.tabs[9].(*inventoryTab)
	var labels []string
	for _, r := range inv.rows {
		labels = append(labels, r.node.label+" "+r.node.info)
	}
	all := strings.Join(labels, "\n")
	// problem branches are open, so the failing parts are visible
	for _, want := range []string{"Processors", "cpu7", "Channel B", "P2-DIMMB1", "VD 0/0", "PD 252:2", "md1", "sdd1", "bond0", "ens1f1", "GPU1"} {
		if !strings.Contains(all, want) {
			t.Errorf("tree missing %q\n%s", want, all)
		}
	}
	// a healthy branch stays closed
	if strings.Contains(all, "P1-DIMMA1") {
		t.Errorf("healthy channel should be closed\n%s", all)
	}
	// healthy branches stay closed with a problem count where relevant
	if !strings.Contains(s, "▸") || !strings.Contains(s, "▾") {
		t.Errorf("tree open/closed markers\n%s", s)
	}
	for i, it := range inv.tlist.shown {
		if strings.HasSuffix(it.key, "/P2-DIMMB1") {
			inv.tlist.setSel(i)
		}
	}
	run(m, "enter")
	if m.tabs[m.active].Title() != "Memory" || !strings.Contains(text(m), "│ ✖ P2-DIMMB1") {
		t.Errorf("tree enter -> %s\n%s", m.tabs[m.active].Title(), text(m))
	}

	run(m, "v") // memory slot map
	if s := text(m); !strings.Contains(s, "Slot map") || !strings.Contains(s, "▌ch B  ▕✖ 32G") || !strings.Contains(s, "┄ empty ┄") || !strings.Contains(s, "◂ 1 uncorrectable ECC") {
		t.Errorf("slot map\n%s", s)
	}
	checkFits(t, "slot map", screen(m), 140, 60)

	run(m, "?")
	if s := text(m); !strings.Contains(s, "Navigation") || !strings.Contains(s, "problems only") || !strings.Contains(s, "Any key closes") ||
		!strings.Contains(s, "This tab · Memory") || !strings.Contains(s, "a shows or hides empty slots") {
		t.Errorf("help overlay\n%s", s)
	}
	checkFits(t, "help", screen(m), 140, 60)
	run(m, "x")
	if m.showHelp {
		t.Error("help did not close")
	}

	// mouse: click the "4 Storage" tab
	m.View()
	s = text(m)
	line := strings.Split(s, "\n")[1]
	x := strings.Index(line, "4 Storage")
	m.Update(tea.MouseClickMsg{X: ansi.StringWidth(line[:x]), Y: 1, Button: tea.MouseLeft})
	if m.tabs[m.active].Title() != "Storage" {
		t.Errorf("click at %d -> %s", x, m.tabs[m.active].Title())
	}
	mm := newLoaded(t, "server-degraded", 80, 24)
	mm.opts.Mouse = true
	if v := mm.View(); v.MouseMode != tea.MouseModeCellMotion || !v.ReportFocus {
		t.Error("mouse mode not enabled with --mouse")
	}
}

func TestWindowTitleShowsStatus(t *testing.T) {
	m := newLoaded(t, "server-degraded", 120, 40)
	if title := m.View().WindowTitle; !strings.HasPrefix(title, "CRIT srv-degraded-01 (25 crit, 24 warn)") {
		t.Errorf("title %q", title)
	}
}

func TestCopySerial(t *testing.T) {
	m := newLoaded(t, "server-degraded", 120, 40)
	focus(t, m, "4", "sdd")
	if _, cmd := m.Update(press("c")); cmd == nil || !strings.Contains(m.message, "WJG0A004") {
		t.Errorf("c on sdd: cmd=%v message %q", cmd != nil, m.message)
	}
	// the footer shows the serial (fallback when the terminal ignores OSC 52)
	if s := text(m); !strings.Contains(s, "copied S/N WJG0A004") {
		t.Errorf("footer\n%s", s)
	}
	// the inventory tree has a Serial column and an inspector
	run(m, "0", "v")
	if s := text(m); !strings.Contains(s, "Serial") || !strings.Contains(s, "S412345X3A01234") || !strings.Contains(s, "copy serial") || !strings.Contains(s, "Path") {
		t.Errorf("tree serial/inspector\n%s", s)
	}
	if _, cmd := m.Update(press("c")); cmd == nil || !strings.Contains(m.message, "S412345X3A01234") {
		t.Errorf("c in tree: message %q", m.message)
	}
}

func TestPowerAndEvents(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {120, 40}, {160, 50}} {
		m := newLoaded(t, "server-degraded", size[0], size[1])
		run(m, "5")
		s := text(m)
		for _, want := range []string{"1/2 PSUs OK · 412 W of 2400 W", "PSU2", "0 V", "Critical", "BMC Redfish"} {
			if !strings.Contains(s, want) {
				t.Errorf("%dx%d power missing %q\n%s", size[0], size[1], want, s)
			}
		}
		checkFits(t, "power", screen(m), size[0], size[1])
		run(m, "8")
		checkFits(t, "events", screen(m), size[0], size[1])
	}
	m := newLoaded(t, "server-degraded", 120, 40)
	run(m, "5")
	if s := text(m); !strings.Contains(s, "Total draw  412 W of 2400 W capacity") || !strings.Contains(s, "Redundancy  Critical") || !strings.Contains(s, "6 of 6 reporting") {
		t.Errorf("power totals\n%s", s)
	}
	run(m, "enter") // the critical supply is listed first
	if m.page == nil || !strings.Contains(text(m), "Power › PSU2") || !strings.Contains(text(m), "no AC input") {
		t.Errorf("psu page\n%s", text(m))
	}
	run(m, "c")
	if !strings.Contains(m.message, "P1K22CL21123452") {
		t.Errorf("copy psu serial: %q", m.message)
	}
	run(m, "esc", "8")
	s := text(m)
	for _, want := range []string{"Severity", "Source", "Logs  System Event Log 6 · BMC Event Log 1 · kernel log", "PS2 Status: Power Supply AC lost",
		"Findings · Events 3 ✖ · 2 ▲"} {
		if !strings.Contains(s, want) {
			t.Errorf("events missing %q\n%s", want, s)
		}
	}
	ev := m.tabs[7].(*eventsTab)
	all := len(ev.list.shown)
	run(m, "s") // BMC only
	if ev.src != 1 || len(ev.list.shown) != 7 || m.tabs[m.active].Title() != "Events" {
		t.Errorf("BMC filter: src %d rows %d of %d", ev.src, len(ev.list.shown), all)
	}
	run(m, "f", "f") // critical only
	if ev.sev != 2 || len(ev.list.shown) != 3 {
		t.Errorf("crit filter: %d rows", len(ev.list.shown))
	}
	run(m, "/", "A", "C", " ", "l", "o", "s", "t", "enter")
	if ev.search.Value() != "AC lost" || len(ev.list.shown) != 1 {
		t.Errorf("search %q: %d rows", ev.search.Value(), len(ev.list.shown))
	}
	run(m, "space")
	if !ev.expand || !strings.Contains(text(m), "Power Supply · Power Supply") && !strings.Contains(text(m), "PS2 Status") {
		t.Errorf("details\n%s", text(m))
	}
	run(m, "enter")
	if m.page == nil || !strings.Contains(text(m), "PS2 Status: Power Supply AC lost") {
		t.Errorf("event page\n%s", text(m))
	}
	run(m, "esc", "esc")
	if ev.search.Value() != "" || ev.sev != 0 || ev.src != 0 || len(ev.list.shown) != all {
		t.Errorf("esc did not clear the filters: %d of %d", len(ev.list.shown), all)
	}
}

func TestHelpEveryTab(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {120, 40}, {160, 50}} {
		m := newLoaded(t, "server-degraded", size[0], size[1])
		for i, tb := range m.tabs {
			if _, ok := tb.(helpTexter); !ok {
				t.Errorf("%s has no help text", tb.Title())
			}
			m.active = i
			run(m, "?")
			s := text(m)
			if !strings.Contains(s, "This tab · "+tb.Title()) || !strings.Contains(s, "Any key closes") {
				t.Errorf("%dx%d help for %s\n%s", size[0], size[1], tb.Title(), s)
			}
			checkFits(t, "help "+tb.Title(), screen(m), size[0], size[1])
			run(m, "x") // any key closes, and is not passed on
			if m.showHelp || m.active != i {
				t.Errorf("help did not close cleanly on %s", tb.Title())
			}
		}
	}
}

func TestSelectionFollowsIDAndReportsLoss(t *testing.T) {
	env, _, err := fixture.Load(fixtures+"server-degraded", collector.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	m := newLoaded(t, "server-degraded", 120, 40)
	focus(t, m, "3", "P2-DIMMC1")
	// an unchanged refresh keeps the selection and says nothing
	m.Update(snapshotMsg{snap: collector.Collect(context.Background(), env, all.Collectors(), nil)})
	text(m)
	if it, _ := m.tabs[2].(*memoryTab).list.current(); it.key != "P2-DIMMC1" || strings.Contains(m.message, "no longer") {
		t.Errorf("refresh moved the selection to %q / message %q", it.key, m.message)
	}
	// the DIMM vanishes: the next row takes the selection, the footer says so
	snap := collector.Collect(context.Background(), env, all.Collectors(), nil)
	var keep []model.DIMM
	for _, d := range snap.Memory.DIMMs {
		if d.Locator != "P2-DIMMC1" {
			keep = append(keep, d)
		}
	}
	snap.Memory.DIMMs = keep
	m.Update(snapshotMsg{snap: snap})
	if s := text(m); !strings.Contains(s, "P2-DIMMC1 no longer reported") {
		t.Errorf("lost selection not reported\n%s", s)
	}
}

// generate presses ↓ until the Generate button and enter.
func generate(t *testing.T, m *Model) {
	t.Helper()
	for m.reports != nil && m.reports.fields()[m.reports.field] != "generate" {
		run(m, "down")
	}
	run(m, "enter")
}

func TestReportsDialog(t *testing.T) {
	m := newLoaded(t, "server-degraded", 120, 40)
	run(m, "R")
	if s := text(m); m.reports == nil || !strings.Contains(s, "Reports") || !strings.Contains(s, "Host report") || !strings.Contains(s, "Generate") {
		t.Fatalf("dialog\n%s", s)
	}
	checkFits(t, "reports dialog", screen(m), 120, 40)
	// host report, viewed, searchable
	generate(t, m)
	if m.page == nil || !strings.Contains(text(m), "srv-degraded-01 · hardware report") {
		t.Fatalf("host report page\n%s", text(m))
	}
	run(m, "/", "P", "2", "-", "D", "I", "M", "M", "B", "1", "enter")
	if !strings.Contains(text(m), "match 1 of") {
		t.Errorf("search in report\n%s", text(m))
	}
	run(m, "esc", "esc")

	// saved as Markdown (Output: Save)
	run(m, "R", "down", "down", "right")
	generate(t, m)
	saved := strings.TrimPrefix(m.message, "saved ")
	if b, err := os.ReadFile(saved); err != nil || !strings.HasSuffix(saved, ".md") || !strings.Contains(string(b), "## Alerts") || m.page != nil {
		t.Fatalf("save: %q %v", m.message, err)
	}

	// JSON, view + save
	run(m, "R", "down", "right", "down", "right", "right", "right")
	generate(t, m)
	if m.page == nil || !strings.Contains(text(m), "schema_version") || !strings.HasSuffix(m.message, ".json") {
		t.Fatalf("json view+save: %q\n%s", m.message, text(m))
	}
	run(m, "esc")

	// Output: Copy puts the report on the clipboard, no page, no file
	run(m, "R", "down", "down", "right", "right")
	for m.reports.fields()[m.reports.field] != "generate" {
		run(m, "down")
	}
	if _, cmd := m.Update(press("enter")); cmd == nil || m.page != nil || !strings.Contains(m.message, "copied the report (Markdown") {
		t.Fatalf("copy output: cmd %v page %v %q", cmd != nil, m.page != nil, m.message)
	}
	// c on a report page copies the report; on a component page the serial
	run(m, "R")
	generate(t, m)
	m.message = "" // the copy toast of the step before has expired
	if !strings.Contains(text(m), "c copy report") {
		t.Errorf("footer hint\n%s", text(m))
	}
	if _, cmd := m.Update(press("c")); cmd == nil || !strings.Contains(m.message, "copied the report") {
		t.Errorf("c on report page: %q", m.message)
	}
	run(m, "esc")
	focus(t, m, "4", "sdd")
	run(m, "enter")
	if _, cmd := m.Update(press("c")); cmd == nil || !strings.Contains(m.message, "copied S/N WJG0A004") {
		t.Errorf("c on disk page: %q", m.message)
	}
	run(m, "esc")

	// history from the export directory: the two saved files + a snapshot export
	run(m, "e", "R", "right", "right")
	if !strings.Contains(m.reports.paths, m.opts.ExportDir) {
		t.Errorf("history sources %q", m.reports.paths)
	}
	generate(t, m)
	if s := text(m); m.page == nil || !strings.Contains(s, "srv-degraded-01 · history") {
		t.Fatalf("history\n%s", s)
	}
	run(m, "esc")

	// history from a path without JSON says so and stays open
	run(m, "R", "right", "right", "down", "enter")
	m.reports.input.SetValue(t.TempDir())
	run(m, "enter")
	generate(t, m)
	if m.reports == nil || !strings.Contains(text(m), "no Tyr snapshots or stress results") {
		t.Errorf("empty history\n%s", text(m))
	}
	run(m, "esc")

	// stress result: none yet, then one from this session
	run(m, "R", "right")
	generate(t, m)
	if m.reports == nil || !strings.Contains(text(m), "no stress results yet") {
		t.Errorf("no runs\n%s", text(m))
	}
	run(m, "esc")
}

func TestReportsStressResult(t *testing.T) {
	saved := durations[0]
	durations[0] = 2 * time.Second
	defer func() { durations[0] = saved }()
	m := newLoaded(t, "server-healthy", 120, 40)
	run(m, "9")
	st := m.stressTab()
	st.env.RunDir = t.TempDir()
	st.duration = 0
	m.Update(st.prepare()())
	st.confirm.SetValue("yes")
	_, cmd := m.Update(press("enter"))
	m.Update(cmd())
	drive(t, m, st, 2, nil)
	// on the result view R starts at "Stress result" with this run
	run(m, "R")
	if m.reports == nil || m.reports.kind != 1 || !strings.Contains(text(m), "CPU · PASS") {
		t.Fatalf("dialog on result\n%s", text(m))
	}
	generate(t, m)
	if s := text(m); m.page == nil || !strings.Contains(s, "CPU stress test") || !strings.Contains(s, "Verdict: ● PASS") {
		t.Fatalf("stress report\n%s", s)
	}
	// the page shows sparklines; what c copies has the Mermaid charts
	if !strings.Contains(m.page.copyText, "```mermaid\nxychart-beta") || strings.Contains(m.page.body, "xychart-beta") {
		t.Errorf("copy text should carry Mermaid, the page sparklines")
	}
}

func TestDocView(t *testing.T) {
	d := &docView{title: "test", markdown: "# Hello\n\n| a | b |\n| --- | --- |\n| 1 | 2 |\n\nfind me\n", dark: true, profile: colorprofile.TrueColor}
	d.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	s := ansi.Strip(d.View().Content)
	if !strings.Contains(s, "Hello") || !strings.Contains(s, "tyr view") || !strings.Contains(s, "q esc quit") {
		t.Fatalf("doc view\n%s", s)
	}
	checkFits(t, "docview", d.View().Content, 80, 24)
	for _, k := range []string{"/", "f", "i", "n", "d", "enter"} {
		d.Update(press(k))
	}
	if s := ansi.Strip(d.View().Content); !strings.Contains(s, "match 1 of 1") {
		t.Errorf("search\n%s", s)
	}
	d.Update(press("esc")) // leaves the search
	if _, cmd := d.Update(press("c")); cmd == nil || !strings.Contains(ansi.Strip(d.View().Content), "copied the document") {
		t.Errorf("c in view\n%s", ansi.Strip(d.View().Content))
	}
	if _, cmd := d.Update(press("q")); cmd == nil {
		t.Error("q did not quit")
	}
}

func TestISCSIVolumeShown(t *testing.T) {
	m := newLoaded(t, "server-degraded", 140, 50)
	focus(t, m, "4", "sdf")
	s := text(m)
	for _, want := range []string{"7 disks", "1 network volume (1 session down)", "sdf      iSCSI IET VIRTUAL-DISK", "iqn.2024-01.net.example:backup.vol2",
		"session1 via 10.16.40.20:3260", "FAILED", "on the storage system"} {
		if !strings.Contains(s, want) {
			t.Errorf("storage tab missing %q\n%s", want, s)
		}
	}
	run(m, "enter")
	p := text(m)
	for _, want := range []string{"Interface  iSCSI", "Portal     10.16.40.20:3260", "a volume of a storage system", "I/O errors", "iscsiadm -m session -P 3",
		"0 new · 465 since boot"} {
		if !strings.Contains(p, want) {
			t.Errorf("volume page missing %q\n%s", want, p)
		}
	}
}
