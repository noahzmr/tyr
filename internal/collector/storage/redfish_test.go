package storage

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/noahzmr/tyr/internal/collector"
	"github.com/noahzmr/tyr/internal/model"
)

type mapGetter map[string]string

func (m mapGetter) Get(_ context.Context, p string) ([]byte, error) {
	if s, ok := m[p]; ok {
		return []byte(s), nil
	}
	return nil, os.ErrNotExist
}

// smartArray is a BMC with one Smart Array controller and these drives.
func smartArray(serial string, drives ...string) mapGetter {
	g := mapGetter{
		"/redfish/v1/Systems": `{"Members":[{"@odata.id":"/redfish/v1/Systems/1/"}]}`,
		"/redfish/v1/Systems/1/SmartStorage/ArrayControllers": `{"Members":[{"@odata.id":"/c/0"}]}`,
		"/c/0": `{"Model":"HPE Smart Array P408i-a SR Gen10","SerialNumber":"` + serial + `","Status":{"Health":"OK"},"Links":{"PhysicalDrives":{"@odata.id":"/c/0/d"}}}`,
	}
	var links []string
	for i, d := range drives {
		p := fmt.Sprintf("/c/0/d/%d", i)
		links = append(links, `{"@odata.id":"`+p+`"}`)
		g[p] = d
	}
	g["/c/0/d"] = `{"Members":[` + strings.Join(links, ",") + `]}`
	return g
}

func TestRedfishRAID(t *testing.T) {
	failed := `{"Location":"2I:1:5","DiskDriveStatusReasons":["DriveFailure"],"Status":{"Health":"Critical"}}`
	worn := `{"Location":"1I:1:4","Model":"MZ7KH960HAJR","SerialNumber":"S47N02","SSDEnduranceUtilizationPercentage":95,"Status":{"Health":"OK"}}`
	visible := `{"Location":"1I:1:3","Model":"VK000960GWSRT","SerialNumber":"S4N01","Status":{"Health":"OK"}}`
	env := func(g mapGetter) *collector.Env { return &collector.Env{Redfish: g, Now: time.Now} }

	// drives behind the controller: added and judged
	out := &model.Storage{Disks: []model.Disk{{Name: "sda", Serial: "S4N01"}}}
	redfishRAID(context.Background(), env(smartArray("PEY01", failed, worn, visible)), out)
	if len(out.Controllers) != 1 || len(out.Controllers[0].PDs) != 3 {
		t.Fatalf("controllers %+v", out.Controllers)
	}
	c := codes(out)
	if len(c["raid_pd"]) != 2 || c["raid_pd"][0].Status != model.StatusCrit || c["raid_pd"][0].Subject != "2I:1:5" ||
		c["raid_pd"][0].Message != "RAID physical drive 2I:1:5 (): Critical, DriveFailure" ||
		c["raid_pd"][1].Status != model.StatusWarn || c["raid_pd"][1].Message != "RAID physical drive 1I:1:4 (MZ7KH960HAJR S47N02): OK, 5% endurance left" {
		t.Errorf("findings %+v", out.Findings)
	}

	// HBA mode: the host sees every drive and there is no logical drive
	out = &model.Storage{Disks: []model.Disk{{Name: "sda", Serial: "S4N01"}}}
	redfishRAID(context.Background(), env(smartArray("PEY01", visible)), out)
	if len(out.Controllers) != 0 {
		t.Errorf("HBA-mode controller listed: %+v", out.Controllers)
	}

	// storcli already reported the controller
	out = &model.Storage{Controllers: []model.RAIDController{{Index: 0, Serial: "PEY01", Tool: "storcli"}}}
	redfishRAID(context.Background(), env(smartArray("PEY01", worn)), out)
	if len(out.Controllers) != 1 {
		t.Errorf("controller doubled: %+v", out.Controllers)
	}
}
