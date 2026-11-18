// Command relnotes writes Tyr's release notes: the highlights written by
// hand in CHANGELOG.md's "Unreleased" section, and the commits since the
// last release grouped by their area ("storage: …" → Storage).
//
//	relnotes -next minor                 print the next version after the last tag
//	relnotes -version v1.2.0             print the notes
//	relnotes -version v1.2.0 -write -notes FILE
//	                                     move Unreleased into a v1.2.0 section of
//	                                     CHANGELOG.md and write the notes to FILE
//	relnotes -section v1.2.0             print that version's section as release notes
//	relnotes -update-release v1.2.0      set the GitHub release's text to that section
//	                                     (GITHUB_TOKEN)
//
// CHANGELOG.md is wrapped for reading in an editor; the release notes join
// the wrapped lines, since GitHub shows a release's line breaks as they are.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

func main() {
	next := flag.String("next", "", "print the version after the last tag: patch, minor or major")
	version := flag.String("version", "", "the version being released (vX.Y.Z)")
	prev := flag.String("prev", "", "the previous release tag (default: the latest vX.Y.Z tag)")
	changelog := flag.String("changelog", "CHANGELOG.md", "the changelog file")
	write := flag.Bool("write", false, "update the changelog")
	notes := flag.String("notes", "", "write the release notes to this file (default: stdout)")
	date := flag.String("date", time.Now().UTC().Format("2006-01-02"), "the release date")
	repo := flag.String("repo", "", "https URL of the repository for commit links (default: from origin)")
	needHighlights := flag.Bool("require-highlights", false, "fail when the Unreleased section is empty")
	sectionOf := flag.String("section", "", "print this version's CHANGELOG section as release notes")
	update := flag.String("update-release", "", "set this version's GitHub release text to its CHANGELOG section")
	flag.Parse()
	if *sectionOf != "" || *update != "" {
		if err := sectionCmd(*sectionOf, *update, *changelog, *repo); err != nil {
			fmt.Fprintln(os.Stderr, "relnotes:", err)
			os.Exit(1)
		}
		return
	}
	if err := run(*next, *version, *prev, *changelog, *write, *notes, *date, *repo, *needHighlights); err != nil {
		fmt.Fprintln(os.Stderr, "relnotes:", err)
		os.Exit(1)
	}
}

func run(next, version, prev, changelog string, write bool, notesFile, date, repo string, needHighlights bool) error {
	if prev == "" {
		prev = lastTag()
	}
	if next != "" {
		v, err := Bump(prev, next)
		if err != nil {
			return err
		}
		fmt.Println(v)
		return nil
	}
	if _, err := ParseSemver(version); err != nil {
		return err
	}
	if prev != "" {
		if !Less(prev, version) {
			return fmt.Errorf("%s is not newer than the last release %s", version, prev)
		}
	}
	if repo == "" {
		repo = repoURL()
	}
	cl, err := os.ReadFile(changelog)
	if err != nil {
		return err
	}
	highlights := Unreleased(string(cl))
	if needHighlights && highlights == "" {
		return fmt.Errorf("the Unreleased section of %s is empty: write the highlights of %s first", changelog, version)
	}
	commits, err := Commits(prev)
	if err != nil {
		return err
	}
	if len(commits) == 0 {
		return fmt.Errorf("no commits since %s", prev)
	}
	body := Notes(highlights, commits, repo, prev, version)
	if write {
		updated, err := Release(string(cl), version, date, body)
		if err != nil {
			return err
		}
		if err := os.WriteFile(changelog, []byte(updated), 0o644); err != nil {
			return err
		}
	}
	if notesFile != "" {
		return os.WriteFile(notesFile, []byte(Unwrap(body)), 0o644)
	}
	fmt.Print(Unwrap(body))
	return nil
}

func sectionCmd(version, update, changelog, repo string) error {
	if update != "" {
		version = update
	}
	cl, err := os.ReadFile(changelog)
	if err != nil {
		return err
	}
	body, err := Section(string(cl), version)
	if err != nil {
		return err
	}
	body = Unwrap(body)
	if update == "" {
		fmt.Print(body)
		return nil
	}
	if repo == "" {
		repo = repoURL()
	}
	return UpdateRelease(repo, version, body, os.Getenv("GITHUB_TOKEN"))
}

// Section returns the body of a version's section in the changelog.
func Section(cl, version string) (string, error) {
	head := "## [" + strings.TrimPrefix(version, "v") + "]"
	i := strings.Index(cl, "\n"+head)
	if i < 0 {
		return "", fmt.Errorf("CHANGELOG.md has no section for %s", version)
	}
	body := cl[i+1:]
	if nl := strings.IndexByte(body, '\n'); nl >= 0 {
		body = body[nl+1:]
	}
	if j := strings.Index(body, "\n## "); j >= 0 {
		body = body[:j+1]
	}
	return strings.TrimSpace(body) + "\n", nil
}

// Unwrap joins the lines a paragraph or list item was wrapped into, and
// leaves headings, list markers, quotes, tables, comments and code alone.
func Unwrap(md string) string {
	var out []string
	fence, joinable := false, false
	for _, line := range strings.Split(md, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "```") {
			fence = !fence
			out, joinable = append(out, line), false
			continue
		}
		if fence {
			out = append(out, line)
			continue
		}
		block := t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, ">") || strings.HasPrefix(t, "|") ||
			strings.HasPrefix(t, "<") || listItem.MatchString(t)
		if joinable && !block {
			out[len(out)-1] += " " + t
			continue
		}
		out = append(out, line)
		joinable = t != "" && !strings.HasPrefix(t, "#") && !strings.HasPrefix(t, "|") && !strings.HasPrefix(t, "<")
	}
	return strings.Join(out, "\n")
}

var listItem = regexp.MustCompile(`^([-*+]|\d+[.)])\s`)

// apiBase is GitHub's API (tests point it at a fake).
var apiBase = "https://api.github.com"

// UpdateRelease replaces the text of a GitHub release.
func UpdateRelease(repo, tag, body, token string) error {
	if token == "" {
		return errors.New("GITHUB_TOKEN is not set")
	}
	owner, name, ok := strings.Cut(strings.TrimPrefix(repo, "https://github.com/"), "/")
	if !ok || strings.Contains(name, "/") {
		return fmt.Errorf("%s is not a GitHub repository", repo)
	}
	api := apiBase + "/repos/" + owner + "/" + name
	call := func(method, url string, in any, out any) error {
		var rd io.Reader
		if in != nil {
			b, _ := json.Marshal(in)
			rd = bytes.NewReader(b)
		}
		req, err := http.NewRequest(method, url, rd)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		req.Header.Set("User-Agent", "tyr-release")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if resp.StatusCode/100 != 2 {
			return fmt.Errorf("%s %s: %s: %s", method, url, resp.Status, strings.TrimSpace(string(b)))
		}
		if out != nil {
			return json.Unmarshal(b, out)
		}
		return nil
	}
	var rel struct {
		ID      int64  `json:"id"`
		HTMLURL string `json:"html_url"`
	}
	if err := call("GET", api+"/releases/tags/"+tag, nil, &rel); err != nil {
		return err
	}
	if err := call("PATCH", fmt.Sprintf("%s/releases/%d", api, rel.ID), map[string]string{"body": body}, nil); err != nil {
		return err
	}
	fmt.Println("updated the text of", rel.HTMLURL)
	return nil
}

// --- semver -------------------------------------------------------------------

var semverRE = regexp.MustCompile(`^v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-([0-9A-Za-z.-]+))?$`)

// ParseSemver splits vMAJOR.MINOR.PATCH[-PRE].
func ParseSemver(v string) ([3]int, error) {
	m := semverRE.FindStringSubmatch(v)
	if m == nil {
		return [3]int{}, fmt.Errorf("%q is not a semantic version (vMAJOR.MINOR.PATCH)", v)
	}
	var out [3]int
	for i := range out {
		out[i], _ = strconv.Atoi(m[i+1])
	}
	return out, nil
}

// Less compares two versions; a pre-release sorts before its release.
func Less(a, b string) bool {
	x, _ := ParseSemver(a)
	y, _ := ParseSemver(b)
	for i := range x {
		if x[i] != y[i] {
			return x[i] < y[i]
		}
	}
	pa, pb := strings.Contains(a, "-"), strings.Contains(b, "-")
	return pa && !pb
}

// Bump returns the next version; without a previous release it is v1.0.0.
func Bump(prev, part string) (string, error) {
	if prev == "" {
		return "v1.0.0", nil
	}
	v, err := ParseSemver(prev)
	if err != nil {
		return "", err
	}
	switch part {
	case "major":
		v = [3]int{v[0] + 1, 0, 0}
	case "minor":
		v = [3]int{v[0], v[1] + 1, 0}
	case "patch":
		v[2]++
	default:
		return "", fmt.Errorf("bump %q: use patch, minor or major", part)
	}
	return fmt.Sprintf("v%d.%d.%d", v[0], v[1], v[2]), nil
}

// --- git ----------------------------------------------------------------------

func git(args ...string) (string, error) {
	var out, errb bytes.Buffer
	cmd := exec.Command("git", args...)
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// lastTag is the newest vX.Y.Z tag reachable from HEAD ("" before the first).
func lastTag() string {
	out, err := git("tag", "--merged", "HEAD", "--list", "v*")
	if err != nil {
		return ""
	}
	var tags []string
	for _, t := range strings.Fields(out) {
		if _, err := ParseSemver(t); err == nil {
			tags = append(tags, t)
		}
	}
	sort.Slice(tags, func(i, j int) bool { return Less(tags[i], tags[j]) })
	if len(tags) == 0 {
		return ""
	}
	return tags[len(tags)-1]
}

// repoURL turns origin (git@github.com:owner/repo.git) into an https URL.
func repoURL() string {
	out, err := git("remote", "get-url", "origin")
	if err != nil {
		return ""
	}
	u := strings.TrimSuffix(strings.TrimSpace(out), ".git")
	if rest, ok := strings.CutPrefix(u, "git@"); ok {
		u = "https://" + strings.Replace(rest, ":", "/", 1)
	}
	return u
}

// releaseCommit is the commit make publish makes for a version.
var releaseCommit = regexp.MustCompile(`^release: v\d+\.\d+\.\d+`)

// Commit is one commit of the release.
type Commit struct {
	Hash    string
	Subject string
}

// Commits lists the commits after prev (all of them before the first
// release), oldest first, without merges.
func Commits(prev string) ([]Commit, error) {
	rng := "HEAD"
	if prev != "" {
		rng = prev + "..HEAD"
	}
	out, err := git("log", "--no-merges", "--reverse", "--format=%H%x00%s", rng)
	if err != nil {
		return nil, err
	}
	var cs []Commit
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		h, s, ok := strings.Cut(line, "\x00")
		if ok && !releaseCommit.MatchString(s) {
			cs = append(cs, Commit{Hash: h, Subject: s})
		}
	}
	return cs, nil
}

// --- notes --------------------------------------------------------------------

// areas maps a commit's prefix to its section; order is the sections' order.
var areas = []struct {
	title    string
	prefixes []string
}{
	{"Storage", []string{"storage", "fsck", "smart", "raid", "nvme", "iscsi", "longhorn"}},
	{"CPU and memory", []string{"cpu", "torture", "memory", "workload", "mce"}},
	{"Network", []string{"network", "nic", "vpn", "lldp"}},
	{"System, power and BMC", []string{"system", "smbios", "power", "redfish", "ipmi", "bmc", "events", "gpu", "accel"}},
	{"Stress, acceptance and ISO", []string{"stress", "accept", "iso", "isobuild"}},
	{"TUI", []string{"tui", "overview"}},
	{"Reports", []string{"reports", "report", "history"}},
	{"NetBox", []string{"netbox"}},
	{"Webhooks", []string{"notify", "webhooks"}},
	{"Documentation", []string{"docs", "readme"}},
	{"Build and CI", []string{"ci", "hooks", "init", "build", "make", "release"}},
}

const otherArea = "Other changes"

var prefixRE = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9_/-]*):\s+(.+)$`)

// Classify returns a commit's section and its summary without the prefix.
func Classify(subject string) (string, string) {
	m := prefixRE.FindStringSubmatch(subject)
	if m == nil {
		return otherArea, subject
	}
	p := strings.ToLower(m[1])
	if i := strings.IndexByte(p, '/'); i > 0 {
		p = p[:i]
	}
	for _, a := range areas {
		for _, x := range a.prefixes {
			if p == x {
				return a.title, m[2]
			}
		}
	}
	return otherArea, subject
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	word, _, _ := strings.Cut(s, " ")
	if word != strings.ToLower(word) { // iSCSI, eBPF, ioerr_cnt stay as written
		return s
	}
	r := []rune(s)
	if r[0] >= 'a' && r[0] <= 'z' {
		r[0] -= 'a' - 'A'
	}
	return string(r)
}

// Notes renders the highlights and the grouped commits.
func Notes(highlights string, commits []Commit, repo, prev, version string) string {
	groups := map[string][]string{}
	for _, c := range commits {
		area, sum := Classify(c.Subject)
		ref := "`" + c.Hash[:7] + "`"
		if repo != "" {
			ref = fmt.Sprintf("[`%s`](%s/commit/%s)", c.Hash[:7], repo, c.Hash)
		}
		groups[area] = append(groups[area], fmt.Sprintf("- %s (%s)", upperFirst(sum), ref))
	}
	var b strings.Builder
	if highlights != "" {
		b.WriteString(highlights + "\n\n")
	}
	b.WriteString("### Changes\n")
	for _, a := range append(areas, struct {
		title    string
		prefixes []string
	}{title: otherArea}) {
		if len(groups[a.title]) == 0 {
			continue
		}
		b.WriteString("\n#### " + a.title + "\n\n" + strings.Join(groups[a.title], "\n") + "\n")
	}
	switch {
	case repo != "" && prev != "":
		fmt.Fprintf(&b, "\nAll changes: [%s...%s](%s/compare/%s...%s)\n", prev, version, repo, prev, version)
	case prev == "":
		fmt.Fprintf(&b, "\nThe first release: %d commits.\n", len(commits))
	}
	return b.String()
}

// --- CHANGELOG.md -------------------------------------------------------------

const unreleasedHeading = "## Unreleased"

// unreleasedTemplate is what the Unreleased section holds after a release.
const unreleasedTemplate = "<!-- Highlights and upgrade notes of the next release; `make publish` moves\nthem into its section and adds the commits grouped by area. -->\n"

// section finds the Unreleased section: the start of its body and its end.
func section(cl string) (int, int, error) {
	i := strings.Index(cl, "\n"+unreleasedHeading+"\n")
	if i < 0 {
		if strings.HasPrefix(cl, unreleasedHeading+"\n") {
			i = -1
		} else {
			return 0, 0, errors.New("CHANGELOG.md has no \"## Unreleased\" section")
		}
	}
	start := i + 1 + len(unreleasedHeading) + 1
	end := len(cl)
	if j := strings.Index(cl[start:], "\n## "); j >= 0 {
		end = start + j + 1
	}
	return start, end, nil
}

var commentRE = regexp.MustCompile(`(?s)<!--.*?-->`)

// Unreleased returns the hand-written highlights (comments left out).
func Unreleased(cl string) string {
	start, end, err := section(cl)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(commentRE.ReplaceAllString(cl[start:end], ""))
}

// Release turns the Unreleased section into the version's section with the
// notes, and leaves an empty Unreleased section above it.
func Release(cl, version, date, notes string) (string, error) {
	start, end, err := section(cl)
	if err != nil {
		return "", err
	}
	if strings.Contains(cl, "\n## ["+strings.TrimPrefix(version, "v")+"]") {
		return "", fmt.Errorf("CHANGELOG.md already has a section for %s", version)
	}
	sec := fmt.Sprintf("## [%s] - %s\n\n%s", strings.TrimPrefix(version, "v"), date, strings.TrimRight(notes, "\n")+"\n")
	rest := cl[end:]
	if rest != "" {
		sec += "\n"
	}
	return cl[:start] + "\n" + unreleasedTemplate + "\n" + sec + rest, nil
}
