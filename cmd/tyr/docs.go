package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/noahzmr/tyr/internal/report"
	"github.com/noahzmr/tyr/internal/tui"
)

// reportFrom renders a saved snapshot or stress result.
func reportFrom(path, format string, stdout, stderr io.Writer) int {
	d, err := report.LoadOne(path)
	if err != nil {
		fmt.Fprintln(stderr, "tyr report:", err)
		return 3
	}
	if d.Run != nil {
		switch format {
		case "json":
			return writeJSON(stdout, stderr, d.Run, d.Run.Verdict.ExitCode())
		default: // text and markdown: the Markdown report
			fmt.Fprint(stdout, report.Run(d.Run))
		}
		return d.Run.Verdict.ExitCode()
	}
	switch format {
	case "json":
		return writeJSON(stdout, stderr, d.Snap, d.Snap.Status.ExitCode())
	case "markdown", "md":
		fmt.Fprint(stdout, report.Snapshot(d.Snap))
	default:
		writeText(stdout, d.Snap)
	}
	return d.Snap.Status.ExitCode()
}

func writeJSON(stdout, stderr io.Writer, v any, code int) int {
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		fmt.Fprintln(stderr, "tyr:", err)
		return 3
	}
	return code
}

// loadDocs loads files/directories and keeps one host when asked.
func loadDocs(paths []string, host string, stderr io.Writer, cmd string) ([]report.Doc, []report.Skipped, bool) {
	if len(paths) == 0 {
		fmt.Fprintf(stderr, "tyr %s: give JSON files or directories (snapshots from `tyr report --format json` or the TUI export, stress results)\n", cmd)
		return nil, nil, false
	}
	docs, skipped, err := report.Load(paths)
	if err != nil {
		fmt.Fprintf(stderr, "tyr %s: %v\n", cmd, err)
		return nil, nil, false
	}
	if host != "" {
		var keep []report.Doc
		for _, d := range docs {
			if d.Host() == host {
				keep = append(keep, d)
			}
		}
		docs = keep
	}
	if len(docs) == 0 {
		fmt.Fprintf(stderr, "tyr %s: no Tyr snapshots or stress results found", cmd)
		if host != "" {
			fmt.Fprintf(stderr, " for host %s", host)
		}
		fmt.Fprintln(stderr)
		for _, s := range skipped {
			fmt.Fprintf(stderr, "  skipped %s: %s\n", s.Path, s.Reason)
		}
		return nil, nil, false
	}
	return docs, skipped, true
}

// runHistory: tyr history [--format markdown|json] [--host NAME] PATH...
func runHistory(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("tyr history", flag.ContinueOnError)
	fs.SetOutput(stderr)
	format := fs.String("format", "markdown", "output format: markdown or json")
	host := fs.String("host", "", "only this host (default: every host in the files)")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "Usage: tyr history [--format markdown|json] [--host NAME] PATH...\n\n"+
			"History of hosts from saved JSON: snapshots (tyr report --format json, the TUI export)\n"+
			"and stress results. Directories are searched for *.json. Shows the status over time,\n"+
			"when each alert appeared and resolved, counter and sensor trends, hardware and\n"+
			"firmware changes, and the stress tests.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 3
	}
	docs, skipped, ok := loadDocs(fs.Args(), *host, stderr, "history")
	if !ok {
		return 3
	}
	hs := report.BuildHistory(docs)
	switch *format {
	case "json":
		return writeJSON(stdout, stderr, hs, 0)
	case "markdown", "md":
		fmt.Fprint(stdout, report.HistoryMarkdown(hs, skipped))
		return 0
	}
	fmt.Fprintf(stderr, "tyr history: unknown --format %q\n", *format)
	return 3
}

// runView shows a document in the terminal: a Markdown file as it is, one
// JSON document as its report, several as their history.
func runView(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("tyr view", flag.ContinueOnError)
	fs.SetOutput(stderr)
	host := fs.String("host", "", "only this host (several documents)")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "Usage: tyr view [--host NAME] PATH...\n\n"+
			"A .md file is shown as it is, one snapshot or stress result as its report,\n"+
			"several files or a directory as their history. Without a terminal the\n"+
			"Markdown is printed instead.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 3
	}
	paths := fs.Args()
	var title, md string
	switch {
	case len(paths) == 1 && strings.EqualFold(filepath.Ext(paths[0]), ".md"):
		b, err := os.ReadFile(paths[0])
		if err != nil {
			fmt.Fprintln(stderr, "tyr view:", err)
			return 3
		}
		title, md = filepath.Base(paths[0]), string(b)
	default:
		docs, skipped, ok := loadDocs(paths, *host, stderr, "view")
		if !ok {
			return 3
		}
		switch {
		case len(docs) == 1 && len(paths) == 1 && docs[0].Run != nil:
			title, md = "stress result "+docs[0].Host(), report.Run(docs[0].Run)
		case len(docs) == 1 && len(paths) == 1:
			title, md = docs[0].Host()+" report", report.Snapshot(docs[0].Snap)
		default:
			hs := report.BuildHistory(docs)
			names := make([]string, len(hs))
			for i, h := range hs {
				names[i] = h.Host
			}
			title, md = strings.Join(names, ", ")+" history", report.HistoryMarkdown(hs, skipped)
		}
	}
	if !isTTY(os.Stdout) {
		fmt.Fprint(stdout, md)
		return 0
	}
	if err := tui.ViewDocument(title, md); err != nil {
		fmt.Fprintln(stderr, "tyr view:", err)
		return 3
	}
	return 0
}
