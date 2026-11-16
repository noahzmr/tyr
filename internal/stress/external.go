package stress

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/noahzmr/tyr/internal/execx"
)

// runExternal runs an engine binary in a private temp directory until ctx
// ends, logging its output and passing every line to parse. The directory
// and the process group are removed/killed on return.
func runExternal(ctx context.Context, rt Runtime, name string, args []string, parse func(stream, line string)) error {
	dir, err := os.MkdirTemp("", "tyr-"+name+"-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	return runExternalIn(ctx, rt, dir, name, args, parse)
}

func runExternalIn(ctx context.Context, rt Runtime, dir, name string, args []string, parse func(stream, line string)) error {
	rt.Logf("$ %s %s", name, strings.Join(args, " "))
	err := execx.Stream(ctx, dir, func(stream, line string) {
		if strings.TrimSpace(line) == "" {
			return
		}
		rt.Logf("%s: %s", name, line)
		if parse != nil {
			parse(stream, line)
		}
	}, name, args...)
	if ctx.Err() != nil {
		return nil // stopped by duration, user or guard
	}
	return err
}

// --- stress-ng ------------------------------------------------------------------

// stress-ng exit codes: 0 ok, 1 bad args, 2 a stressor failed (e.g.
// verification), 3 out of resources, 4 not implemented, 5 killed by signal.
func stressNGError(err error) error {
	var ee *execx.ExitError
	if !errors.As(err, &ee) {
		return err
	}
	switch ee.Code {
	case 2:
		return &EngineFailure{Msg: "stress-ng reported a failed stressor (exit 2)"}
	case 3:
		return fmt.Errorf("stress-ng ran out of resources (exit 3)")
	}
	return fmt.Errorf("stress-ng exited with %d", ee.Code)
}

var ngMetric = regexp.MustCompile(`metrc:\s*\[\d+\]\s+(\S+)\s+(\d+)\s+[\d.]+\s+[\d.]+\s+[\d.]+\s+([\d.]+)`)

// stressNG runs stress-ng with --verify and the run's duration, turning
// "fail:" lines into verify errors and the metrics table into metrics.
func stressNG(ctx context.Context, cfg Config, rt Runtime, workload string, args ...string) error {
	secs := max(10, int(cfg.Duration.Seconds())+5) // Tyr stops it; this is a backstop
	full := append(args, "--verify", "--metrics-brief", "--timeout", strconv.Itoa(secs)+"s")
	start := time.Now()
	err := runExternal(ctx, rt, EngStressNG, full, func(stream, line string) {
		l := strings.ToLower(line)
		switch {
		case strings.Contains(l, " fail:") || strings.Contains(l, "verification failed") || strings.Contains(l, "verify failed"):
			rt.VerifyError(VerifyError{Subject: workload, Workload: "stress-ng", Detail: strings.TrimSpace(line)})
		case strings.Contains(l, "metrc:"):
			if m := ngMetric.FindStringSubmatch(line); m != nil {
				if v, err := strconv.ParseFloat(m[3], 64); err == nil && time.Since(start) > 0 {
					rt.Metric("bogo_ops_per_s_"+m[1], "ops_per_second", v)
				}
			}
		}
	})
	if err != nil {
		return stressNGError(err)
	}
	return nil
}
