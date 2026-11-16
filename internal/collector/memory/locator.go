package memory

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/noahzmr/tyr/internal/model"
	"github.com/noahzmr/tyr/internal/source/sysfs"
)

// slot is a parsed DIMM position. Socket is 1-based (-1 unknown), Channel a
// letter ("" unknown), Slot 1-based within the channel/socket.
type slot struct {
	Socket  int
	Channel string
	Slot    int
}

func (s slot) group() string {
	switch {
	case s.Channel != "" && s.Socket > 0:
		return fmt.Sprintf("P%d-%s", s.Socket, s.Channel)
	case s.Channel != "":
		return s.Channel
	}
	return ""
}

var locatorPatterns = []struct {
	re *regexp.Regexp
	fn func(m []string) slot
}{
	// Supermicro "P1-DIMMA1", ASRock Rack "CPU1_DIMM_A1", Lenovo "CPU1 DIMM A1"
	{regexp.MustCompile(`(?i)^(?:P|CPU|PROC)\s*(\d+)[\s_-]*DIMM[\s_-]*([A-Z])(\d+)$`), func(m []string) slot {
		return slot{atoi(m[1]), strings.ToUpper(m[2]), atoi(m[3])}
	}},
	// Generic "DIMM_A1", "DIMMA2", "ChannelA-DIMM0"
	{regexp.MustCompile(`(?i)^DIMM[\s_-]?([A-Z])(\d+)$`), func(m []string) slot {
		return slot{-1, strings.ToUpper(m[1]), atoi(m[2])}
	}},
	{regexp.MustCompile(`(?i)^Channel([A-Z])-DIMM(\d+)$`), func(m []string) slot {
		return slot{-1, strings.ToUpper(m[1]), atoi(m[2]) + 1}
	}},
}

// bankPattern: AMD/Gigabyte bank locators "P0_Node0_Channel3_Dimm1".
var bankPattern = regexp.MustCompile(`(?i)^P(\d+)_Node\d+_Channel(\d+|[A-Z])_Dimm(\d+)$`)

// parseLocator derives socket/channel/slot from SMBIOS locators. Dell ("A1",
// where A/B is the socket and channels are not encoded) and HPE ("PROC 1 DIMM
// 12") naming do not identify the channel; those return an empty Channel.
func parseLocator(loc, bank string) slot {
	loc = strings.TrimSpace(loc)
	for _, p := range locatorPatterns {
		if m := p.re.FindStringSubmatch(loc); m != nil {
			s := p.fn(m)
			if s.Socket < 0 {
				if b := bankPattern.FindStringSubmatch(strings.TrimSpace(bank)); b != nil {
					s.Socket = atoi(b[1]) + 1
				}
			}
			return s
		}
	}
	if b := bankPattern.FindStringSubmatch(strings.TrimSpace(bank)); b != nil {
		ch := b[2]
		if n, err := strconv.Atoi(ch); err == nil {
			ch = string(rune('A' + n))
		}
		return slot{atoi(b[1]) + 1, strings.ToUpper(ch), atoi(b[3]) + 1}
	}
	return slot{Socket: -1}
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// Intel EDAC labels: "CPU_SrcID#0_MC#1_Chan#0_DIMM#0" (skx/i10nm) or
// "CPU_SrcID#0_Ha#0_Chan#1_DIMM#0" (sb_edac).
var intelLabel = regexp.MustCompile(`^CPU_SrcID#(\d+)_(?:MC|Ha)#(\d+)_Chan#(\d+)_DIMM#(\d+)$`)

type dimmMatcher struct {
	dimms     []model.DIMM
	parsed    []slot
	chanPerMC map[int]int // socket (0-based) -> channels per controller
}

func newDIMMMatcher(dimms []model.DIMM, mcs []sysfs.EDACController) *dimmMatcher {
	m := &dimmMatcher{dimms: dimms, chanPerMC: map[int]int{}}
	for _, d := range dimms {
		m.parsed = append(m.parsed, parseLocator(d.Locator, d.BankLocator))
	}
	for _, mc := range mcs {
		for _, d := range mc.DIMMs {
			if g := intelLabel.FindStringSubmatch(d.Label); g != nil {
				s, ch := atoi(g[1]), atoi(g[3])
				m.chanPerMC[s] = max(m.chanPerMC[s], ch+1)
			}
		}
	}
	return m
}

// match returns the index of the SMBIOS DIMM an EDAC DIMM belongs to, or -1.
func (m *dimmMatcher) match(d sysfs.EDACDIMM) int {
	label := strings.TrimSpace(d.Label)
	if label == "" {
		return -1
	}
	// 1. Label set by ras-mc-ctl/BIOS equals (or ends with) the SMBIOS locator.
	for i, dm := range m.dimms {
		if !dm.Populated || dm.Locator == "" {
			continue
		}
		if strings.EqualFold(label, dm.Locator) || strings.HasSuffix(strings.ToUpper(label), strings.ToUpper(dm.Locator)) {
			return i
		}
	}
	// 2. Intel topology label -> socket/channel letter/slot.
	g := intelLabel.FindStringSubmatch(label)
	if g == nil {
		return -1
	}
	socket, mc, ch, dimm := atoi(g[1]), atoi(g[2]), atoi(g[3]), atoi(g[4])
	per := m.chanPerMC[socket]
	if per == 0 {
		return -1
	}
	want := slot{Socket: socket + 1, Channel: string(rune('A' + mc*per + ch)), Slot: dimm + 1}
	for i, p := range m.parsed {
		if m.dimms[i].Populated && p == want {
			return i
		}
	}
	return -1
}
