// Package report turns Tyr's JSON documents (snapshots from `tyr report
// --format json` or the TUI export, stress result files) into Markdown
// reports: one host's hardware and alerts, one stress run, and the history
// of a host across many documents. The same data is available as JSON.
package report

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/noahzmr/tyr/internal/model"
	"github.com/noahzmr/tyr/internal/stress"
)

// Doc is one loaded document: a snapshot or a stress result.
type Doc struct {
	Path string
	Snap *model.Snapshot
	Run  *stress.Result
}

// Host is the host the document describes.
func (d Doc) Host() string {
	if d.Run != nil {
		return d.Run.Hostname
	}
	return d.Snap.Hostname
}

// Time is when the document was taken (a run: its start).
func (d Doc) Time() time.Time {
	if d.Run != nil {
		return d.Run.StartedAt
	}
	return d.Snap.Timestamp
}

// Skipped is a file that was not a Tyr document.
type Skipped struct {
	Path, Reason string
}

// Load reads files and directories (recursively, *.json). Files that are
// not Tyr snapshots or stress results are returned as skipped, not as
// errors. Documents come back sorted by time.
func Load(paths []string) ([]Doc, []Skipped, error) {
	var docs []Doc
	var skipped []Skipped
	var files []string
	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil {
			return nil, nil, err
		}
		if !fi.IsDir() {
			files = append(files, p)
			continue
		}
		err = filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() && strings.HasSuffix(strings.ToLower(d.Name()), ".json") {
				files = append(files, path)
			}
			return nil
		})
		if err != nil {
			return nil, nil, err
		}
	}
	for _, f := range files {
		d, why := loadFile(f)
		if why != "" {
			skipped = append(skipped, Skipped{f, why})
			continue
		}
		docs = append(docs, d)
	}
	sort.SliceStable(docs, func(i, j int) bool { return docs[i].Time().Before(docs[j].Time()) })
	return docs, skipped, nil
}

func loadFile(path string) (Doc, string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Doc{}, err.Error()
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(b, &keys); err != nil {
		return Doc{}, "not a JSON object: " + err.Error()
	}
	_, hasRun := keys["run_id"]
	_, hasVerdict := keys["verdict"]
	_, hasHost := keys["hostname"]
	_, hasTime := keys["timestamp"]
	switch {
	case hasRun && hasVerdict:
		var r stress.Result
		if err := json.Unmarshal(b, &r); err != nil {
			return Doc{}, "stress result: " + err.Error()
		}
		r.File = path
		return Doc{Path: path, Run: &r}, ""
	case hasHost && hasTime:
		var s model.Snapshot
		if err := json.Unmarshal(b, &s); err != nil {
			return Doc{}, "snapshot: " + err.Error()
		}
		return Doc{Path: path, Snap: &s}, ""
	}
	return Doc{}, "not a Tyr snapshot or stress result"
}

// Hosts groups documents by host name, in name order.
func Hosts(docs []Doc) ([]string, map[string][]Doc) {
	by := map[string][]Doc{}
	for _, d := range docs {
		by[d.Host()] = append(by[d.Host()], d)
	}
	var names []string
	for n := range by {
		names = append(names, n)
	}
	sort.Strings(names)
	return names, by
}

// LoadOne reads a single document.
func LoadOne(path string) (Doc, error) {
	d, why := loadFile(path)
	if why != "" {
		return Doc{}, fmt.Errorf("%s: %s", path, why)
	}
	return d, nil
}
