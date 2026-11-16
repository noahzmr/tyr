package explain

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/noahzmr/tyr/internal/model"
)

// Every finding code a collector can raise has an explanation.
func TestCatalogCoversCollectorCodes(t *testing.T) {
	// literal codes on Find(...) / add(...) / cnt(...) calls, and code := "…"
	call := regexp.MustCompile(`(?:Find|add|cnt)\([^"\n]*?"([a-z]+(?:_[a-z]+)+)"`)
	assign := regexp.MustCompile(`code(?: :)?= "([a-z]+(?:_[a-z]+)+)"`)
	dirs := map[string]model.ModuleID{"system": model.ModSystem, "cpu": model.ModCPU, "memory": model.ModMemory,
		"storage": model.ModStorage, "network": model.ModNetwork, "accel": model.ModAccel}
	// informational (status OK) codes: no alert, no explanation needed
	info := map[string]bool{"throttle_history": false}
	for dir, mod := range dirs {
		files, _ := filepath.Glob(filepath.Join("..", "collector", dir, "*.go"))
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			for _, re := range []*regexp.Regexp{call, assign} {
				for _, m := range re.FindAllStringSubmatch(string(b), -1) {
					if _, ok := For(mod, m[1]); !ok && !info[m[1]] {
						t.Errorf("%s/%s (%s) has no explanation", mod, m[1], filepath.Base(f))
					}
				}
			}
		}
	}
}

func TestCommands(t *testing.T) {
	e, ok := For(model.ModStorage, "crc_errors")
	if !ok || !strings.Contains(e.Commands("sde")[0], "/dev/sde") || strings.Contains(strings.Join(e.Commands("sde"), " "), "{dev}") {
		t.Errorf("commands %v", e.Commands("sde"))
	}
	if len(All()) < 60 || All()[0].Module != model.ModSystem {
		t.Errorf("All: %d entries, first %v", len(All()), All()[0].Module)
	}
}
