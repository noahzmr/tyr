package system

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/noahzmr/tyr/internal/collector"
	"github.com/noahzmr/tyr/internal/hostfs"
	"github.com/noahzmr/tyr/internal/model"
)

// Boot history from the journal: when each boot started and ended, and
// whether it ended with an orderly shutdown. A boot without journald's
// "Journal stopped" ended in a crash, a hard reset or a power loss.

// journalStopped is the MESSAGE_ID of journald's "Journal stopped".
const journalStopped = "d93fb3c9c24d451a97cea615ce59c00b"

const maxBoots = 20

func readBoots(ctx context.Context, env *collector.Env) ([]model.Boot, model.SourceState) {
	st := model.SourceState{Name: "boot_history", State: model.AvailOK}
	if _, err := env.Exec.LookPath("journalctl"); err != nil {
		st.State, st.Reason = model.AvailUnavailable, "journalctl not installed"
		return nil, st
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := env.Exec.Run(ctx, "journalctl", "--list-boots", "-o", "json", "--no-pager")
	if err != nil && len(out) == 0 {
		st.State, st.Reason = model.AvailUnavailable, "journalctl --list-boots: "+strings.TrimSpace(err.Error())
		return nil, st
	}
	boots := parseBoots(out)
	if len(boots) == 0 {
		st.State, st.Reason = model.AvailUnavailable, "the journal lists no boots"
		return nil, st
	}
	if len(boots) > maxBoots {
		boots = boots[len(boots)-maxBoots:]
	}
	cur := strings.ReplaceAll(strings.TrimSpace(hostfs.StringOr(env.FS, "/proc/sys/kernel/random/boot_id", "")), "-", "")
	if cur != "" {
		for i := range boots {
			boots[i].Current = boots[i].ID == cur
		}
	} else {
		boots[len(boots)-1].Current = true
	}
	// orderly shutdowns
	clean := map[string]bool{}
	if out, err := env.Exec.Run(ctx, "journalctl", "MESSAGE_ID="+journalStopped, "-o", "json", "--output-fields=_BOOT_ID", "--no-pager", "-q"); err == nil || len(out) > 0 {
		for _, ln := range strings.Split(string(out), "\n") {
			var e struct {
				Boot string `json:"_BOOT_ID"`
			}
			if json.Unmarshal([]byte(ln), &e) == nil && e.Boot != "" {
				clean[e.Boot] = true
			}
		}
	}
	if len(clean) == 0 {
		st.Reason = "no \"Journal stopped\" in the journal: clean and unclean shutdowns cannot be told apart"
		return boots, st
	}
	for i := range boots {
		if !boots[i].Current {
			c := clean[boots[i].ID]
			boots[i].Clean = &c
		}
	}
	return boots, st
}

// parseBoots reads `journalctl --list-boots -o json`, or the text form of
// older systemd: "-1 80e751c4… Fri 2026-11-13 13:51:28 CET—Fri 2026-11-13 13:52:29 CET".
func parseBoots(b []byte) []model.Boot {
	var js []struct {
		ID    string `json:"boot_id"`
		First int64  `json:"first_entry"`
		Last  int64  `json:"last_entry"`
	}
	if json.Unmarshal(b, &js) == nil {
		out := make([]model.Boot, 0, len(js))
		for _, j := range js {
			out = append(out, model.Boot{ID: j.ID, Start: time.UnixMicro(j.First).UTC(), End: time.UnixMicro(j.Last).UTC()})
		}
		return out
	}
	var out []model.Boot
	for _, ln := range strings.Split(string(b), "\n") {
		f := strings.Fields(strings.ReplaceAll(ln, "—", " "))
		if len(f) != 10 || len(f[1]) != 32 {
			continue
		}
		if _, err := strconv.Atoi(f[0]); err != nil {
			continue
		}
		start, err1 := time.Parse("Mon 2006-01-02 15:04:05 MST", strings.Join(f[2:6], " "))
		end, err2 := time.Parse("Mon 2006-01-02 15:04:05 MST", strings.Join(f[6:10], " "))
		if err1 != nil || err2 != nil {
			continue
		}
		out = append(out, model.Boot{ID: f[1], Start: start, End: end})
	}
	return out
}

// bootFindings: boots of the last 30 days that ended without an orderly
// shutdown.
func bootFindings(h *model.Header, out *model.System, now time.Time) {
	var bad []string
	n := 0
	for _, b := range out.Boots {
		if b.Current || b.Clean == nil || now.Sub(b.End) > 30*24*time.Hour {
			continue
		}
		n++
		if !*b.Clean {
			bad = append(bad, fmt.Sprintf("%s after %s", b.End.Local().Format("2006-01-02 15:04"), short(b.End.Sub(b.Start))))
		}
	}
	if len(bad) == 0 {
		return
	}
	st := model.StatusWarn
	if len(bad) >= 2 {
		st = model.StatusCrit
	}
	list := bad
	if len(list) > 4 {
		list = list[len(list)-4:] // the latest
	}
	h.Find(st, "unclean_shutdown", "boots", "journal", fmt.Sprintf("%d of %d boots in the last 30 days ended without a clean shutdown (crash, hard reset or power loss): %s",
		len(bad), n, strings.Join(list, ", ")))
}

func short(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd %dh", int(d.Hours())/24, int(d.Hours())%24)
}
