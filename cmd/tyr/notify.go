package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"github.com/noahzmr/tyr/internal/notify"
)

// loadNotifier reads the webhook settings; problems are printed, never
// fatal: a test runs without notifications rather than not at all.
func loadNotifier(path string, off bool, stdout io.Writer) *notify.Notifier {
	if off {
		return nil
	}
	c, err := notify.Load(path)
	if err != nil {
		fmt.Fprintf(stdout, "webhooks: %v (no notifications)\n", err)
		return nil
	}
	if len(c.Webhooks) == 0 {
		return nil
	}
	names := ""
	for i, w := range c.Webhooks {
		if i > 0 {
			names += ", "
		}
		names += w.Label()
	}
	fmt.Fprintf(stdout, "webhooks: %s\n", names)
	return notify.NewNotifier(c, func(f string, a ...any) { fmt.Fprintf(stdout, f+"\n", a...) })
}

func runNotify(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("tyr notify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("webhooks", notify.Path(), "webhook settings (JSON); TYR_WEBHOOK_URL and friends add one more")
	test := fs.Bool("test", false, "send a test notification to every webhook and report the result")
	event := fs.String("event", notify.EvTest, "with --test: the event to send instead (started, step_failed, fail …) to see how it looks")
	validate := fs.Bool("validate", false, "check the settings and exit")
	if err := fs.Parse(args); err != nil {
		return 3
	}
	c, err := notify.Load(*path)
	if err != nil {
		fmt.Fprintln(stderr, "tyr notify:", err)
		return 3
	}
	if len(c.Webhooks) == 0 {
		fmt.Fprintf(stderr, "tyr notify: no webhooks (%s, or TYR_WEBHOOK_URL)\n", *path)
		return 3
	}
	for _, w := range c.Webhooks {
		src := c.Path
		if w.FromEnv {
			src = "environment"
		}
		ev := "all events"
		if len(w.Events) > 0 {
			ev = fmt.Sprint(w.Events)
		}
		fmt.Fprintf(stdout, "%s: %s, %s, %s (%s)\n", w.Label(), notify.Redact(w.URL), orNA(w.Format), ev, src)
	}
	if *validate || !*test {
		return 0
	}
	host, _ := os.Hostname()
	e := notify.New(*event, "acceptance", notify.Machine{Host: host})
	e.Summary = "a test notification from tyr notify --test"
	if *event != notify.EvTest {
		e.Profile, e.Step, e.Status = "quick", "cpu", map[string]string{notify.EvStepFailed: "FAIL", notify.EvStepWarning: "WARN",
			notify.EvPass: "PASS", notify.EvRemarks: "REMARKS", notify.EvFail: "FAIL", notify.EvIncomplete: "INCOMPLETE"}[*event]
		if *event == notify.EvFail {
			e.Failures = []string{"example: DIMM P1-A1 uncorrectable ECC error"}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	res := notify.NewNotifier(c, func(f string, a ...any) { fmt.Fprintf(stdout, "  "+f+"\n", a...) }).Deliver(ctx, e)
	names := make([]string, 0, len(res))
	for k := range res {
		names = append(names, k)
	}
	sort.Strings(names)
	code := 0
	for _, k := range names {
		if res[k] != nil {
			fmt.Fprintf(stdout, "✖ %s: %v\n", k, res[k])
			code = 2
		} else {
			fmt.Fprintf(stdout, "✔ %s: delivered\n", k)
		}
	}
	return code
}
