package web

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hellwand/tasmofleet/internal/discovery"
	"github.com/hellwand/tasmofleet/internal/fwselect"
	"github.com/hellwand/tasmofleet/internal/model"
)

// The result table of a network scan (#811): every device of the chosen filter is a row, the
// firmware reads like the Devices list, and the selection column exists only when a row can be
// selected.

var updateScanFixtures = flag.Bool("update-scan-fixtures", false, "rewrite testdata/scan/*.html, the result markup the e2e layout guard loads")

// scanKnownNames are twenty plain device names, so a fleet-sized scan has the lengths of real names.
var scanKnownNames = []string{
	"Balcony Windowsill", "Balcony Door", "Bedroom Cabinet", "Bedroom Commode", "Dehumidifier Plug",
	"Corridor Commode", "Kitchen Counter", "Kitchen Hood", "Living Room Lamp", "Living Room Shelf",
	"Office Desk", "Office Printer", "Garage Door", "Garage Workbench", "Cellar Pump",
	"Attic Heater", "Terrace Lights", "Guest Room Lamp", "Hallway Mirror", "Workshop Dimmer",
}

var scanFixedTime = time.Date(2026, 11, 13, 20, 0, 0, 0, time.UTC)

// scanEnv serves a finished scan that found `known` devices the inventory already holds, plus the
// extra hits and proposals the caller adds. Every known device runs 16.0.0(release-tasmota).
func scanEnv(t *testing.T, known int, extra func(fd *fakeDiscovery)) *env {
	t.Helper()
	fd := &fakeDiscovery{scan: discovery.ScanStatus{ID: "abc123", State: discovery.ScanDone, Total: 508, Done: 508,
		StartedAt: scanFixedTime.Add(-2 * time.Minute), FinishedAt: scanFixedTime}}
	var devs []model.Device
	for i := 0; i < known; i++ {
		mac := fmt.Sprintf("aa:bb:cc:00:01:%02x", i)
		devs = append(devs, model.Device{ID: model.DeviceID(10 + i), Name: scanKnownNames[i%len(scanKnownNames)], Address: fmt.Sprintf("10.0.0.%d", 21+i), MAC: mac})
		fd.scan.Hits = append(fd.scan.Hits, discovery.Hit{Address: fmt.Sprintf("10.0.0.%d", 21+i), Port: 80, MAC: mac,
			Name: scanKnownNames[i%len(scanKnownNames)], Version: "16.0.0(release-tasmota)", Outcome: "known device"})
	}
	if extra != nil {
		extra(fd)
	}
	// a fixed clock an hour after the scan: the status line says "Today 20:00" whatever day the test runs
	e := setupWith(t, func(d *Deps) { d.Discovery = fd; d.Clock = noonClock{scanFixedTime.Add(time.Hour)} })
	for _, d := range devs {
		e.inv.devs[d.ID] = d
	}
	return e
}

// scanMixed adds one new, one ignored, one address-changed and one password-protected device to the
// known ones: every kind of row the table can hold.
func scanMixed(fd *fakeDiscovery) {
	fd.props = []discovery.Proposal{
		{ID: 4, Version: "v4", Kind: discovery.KindNew, Via: "scan", SeenAt: scanFixedTime, Device: model.Device{Name: "Greenhouse Fan", Address: "10.0.0.90", MAC: "aa:bb:cc:00:02:01"}},
		{ID: 6, Version: "v6", Kind: discovery.KindNew, Via: "scan", SeenAt: scanFixedTime, Device: model.Device{Name: "workshop-ceiling-spotlight-controller-south-wing", Address: "10.0.0.92", MAC: "aa:bb:cc:00:02:03"}},
		{ID: 7, Version: "v7", Kind: discovery.KindAddressChange, Via: "scan", SeenAt: scanFixedTime, Device: model.Device{Name: "Balcony Door", Address: "10.0.0.93", MAC: "aa:bb:cc:00:01:01"}},
	}
	fd.ignored = []discovery.Proposal{{ID: 5, Version: "v5", Kind: discovery.KindNew, Via: "scan", SeenAt: scanFixedTime, Device: model.Device{Name: "Spare Plug", Address: "10.0.0.91", MAC: "aa:bb:cc:00:02:02"}}}
	fd.scan.Hits = append(fd.scan.Hits,
		discovery.Hit{Address: "10.0.0.90", Port: 80, MAC: "aa:bb:cc:00:02:01", Name: "Greenhouse Fan", Version: "16.0.0(release-tasmota32)", Outcome: "new proposal"},
		discovery.Hit{Address: "10.0.0.92", Port: 80, MAC: "aa:bb:cc:00:02:03", Name: "workshop-ceiling-spotlight-controller-south-wing", Version: "15.1.0.2(release-tasmota)", Outcome: "new proposal"},
		discovery.Hit{Address: "10.0.0.91", Port: 80, MAC: "aa:bb:cc:00:02:02", Name: "Spare Plug", Version: "16.0.0(release-minimal)", Outcome: "new proposal"},
		discovery.Hit{Address: "10.0.0.94", Port: 80, Outcome: "needs password"},
	)
	// the known device behind the address change keeps its row as an address change, not as known
	fd.scan.Hits = append(fd.scan.Hits, discovery.Hit{Address: "10.0.0.93", Port: 80, MAC: "aa:bb:cc:00:02:04", Name: "Corridor Mirror", Version: "16.0.0(release-tasmota)", Outcome: "address change"})
	fd.props[2].Device.MAC = "aa:bb:cc:00:02:04"
}

// scanTable returns the markup of the desktop table of a result page.
func scanTable(t *testing.T, body string) string {
	t.Helper()
	i := strings.Index(body, `<div class="add-table add-scan-table">`)
	j := strings.Index(body, `<div class="add-cards">`)
	if i < 0 || j < i {
		t.Fatalf("no scan table in the page: %.300s", body)
	}
	return body[i:j]
}

func scanRows(table string) int { return strings.Count(table, `<td class="name"`) }

// Why: the chips say "All 20" and "Known 20", so a table that lists six rows and then a line "and 14
// more known devices" (which looked like plain text) leaves 14 devices out of reach on the page that
// is meant to show what the scan found. Every row of the chosen filter is listed, in All as in Known.
func TestScanListsEveryRowOfTheChosenFilter(t *testing.T) {
	e := scanEnv(t, 20, nil)
	for _, path := range []string{"/discovery/scan/abc123", "/discovery/scan/abc123?f=known"} {
		_, body := e.do(t, "GET", path, "admin", nil)
		if n := scanRows(scanTable(t, body)); n != 20 {
			t.Errorf("%s lists %d rows, the chips say 20", path, n)
		}
		if strings.Contains(body, "more known devices") {
			t.Errorf("%s hides rows behind an \"and N more\" line", path)
		}
		if n := strings.Count(body, `class="add-card add-scan-card"`); n != 20 {
			t.Errorf("%s: the phone cards list %d devices, the chips say 20", path, n)
		}
	}
}

// Why: the Devices list shows the version as the number ("16.0.0") and keeps the build behind a
// tooltip; the scan table printed Tasmota's raw string, whose build suffix was cut off in a narrow
// column. The scan shows the same number, on the table and on the phone card, and words the build
// with the Devices list's own code (fwselect.DetectInfo1 and firmwareTitle), so the two pages agree
// for every build tag, not only the common ones: a hand-written parser had said "(tasmota32)",
// "(solo1)" and "(tasmota-4M)" where the Devices list says "(tasmota)" and "(4M)".
func TestScanFirmwareReadsLikeTheDevicesList(t *testing.T) {
	versions := []string{
		"16.0.0(release-tasmota)", "16.0.0(release-tasmota32)", "15.1.0.2(release-tasmota)", "16.0.0(release-minimal)",
		"16.0.0(release-solo1)", "16.0.0(release-tasmota-4M)", "16.0.0(release-lvgl-haspmota)",
		"16.0.0.3(a1b2c3d-tasmota)", "16.0.0(tasmota32)",
	}
	e := scanEnv(t, 1, func(fd *fakeDiscovery) {
		for i, v := range versions {
			fd.scan.Hits = append(fd.scan.Hits, discovery.Hit{Address: fmt.Sprintf("10.0.1.%d", 10+i), Port: 80,
				MAC: fmt.Sprintf("aa:bb:cc:00:03:%02x", i), Name: fmt.Sprintf("Probe %d", i), Version: v, Outcome: "new proposal"})
		}
		fd.scan.Hits = append(fd.scan.Hits, discovery.Hit{Address: "10.0.1.99", Port: 80, Outcome: "needs password"})
	})
	_, body := e.do(t, "GET", "/discovery/scan/abc123", "admin", nil)
	table := scanTable(t, body)
	for _, v := range versions {
		id, err := fwselect.DetectInfo1([]byte(fmt.Sprintf(`{"Info1":{"Version":%q}}`, v)), model.Identity{})
		if err != nil {
			t.Fatalf("%s: the Devices list's parser refuses it: %v", v, err)
		}
		want := fmt.Sprintf(`<td class="mono" title="%s">%s</td>`, firmwareTitle(id), id.Version)
		if !strings.Contains(table, want) {
			t.Errorf("%s: table lacks %q", v, want)
		}
	}
	if !strings.Contains(table, `<td class="mono">—</td>`) { // the password-protected device told nothing
		t.Error("a device without a version shows a dash and no tooltip")
	}
	if strings.Contains(strings.ReplaceAll(table, `title="Tasmota`, ""), "(release") {
		t.Error("the raw build suffix is still shown")
	}
	cards := body[strings.Index(body, `<div class="add-cards">`):]
	if !strings.Contains(cards, "10.0.0.21</span> · 16.0.0</span>") || strings.Contains(cards, "(release") {
		t.Errorf("the phone card must carry the same version: %.400s", cards)
	}
}

// Why: when no row can be selected (everything is known or ignored) the checkbox column is an empty
// strip in front of the names. It exists only while a row can be ticked.
func TestScanSelectionColumnExistsOnlyWhenARowCanBeSelected(t *testing.T) {
	selCol := regexp.MustCompile(`class="(c-sel|sel)"`)
	e := scanEnv(t, 3, nil)
	_, body := e.do(t, "GET", "/discovery/scan/abc123", "admin", nil)
	if m := selCol.FindString(scanTable(t, body)); m != "" {
		t.Errorf("nothing can be selected, yet the table has %s", m)
	}
	e = scanEnv(t, 3, scanMixed)
	_, body = e.do(t, "GET", "/discovery/scan/abc123", "admin", nil)
	if got := len(selCol.FindAllString(scanTable(t, body), -1)); got < 3 {
		t.Errorf("a new device can be selected: want the column, col and header cell included, found %d marks", got)
	}
	// the Known filter of a scan with new devices lists nothing selectable either
	_, body = e.do(t, "GET", "/discovery/scan/abc123?f=known", "admin", nil)
	if m := selCol.FindString(scanTable(t, body)); m != "" {
		t.Errorf("the Known filter has nothing to select, yet the table has %s", m)
	}
}

// TestScanResultFixtures keeps testdata/scan/*.html, the markup of the finished-scan results the e2e
// layout guard (e2e/tests/scan-table.spec.ts) loads into the real page, equal to what the templates
// render now. A scan only finds devices on port 80, which no e2e stack can serve, so the guard cannot
// run a real one; this keeps it from testing a stale copy. Regenerate with
// go test ./internal/web -run TestScanResultFixtures -update-scan-fixtures
func TestScanResultFixtures(t *testing.T) {
	for _, tc := range []struct {
		file  string
		known int
		extra func(*fakeDiscovery)
	}{
		{"known.html", 20, nil},
		{"mixed.html", 8, scanMixed},
	} {
		t.Run(tc.file, func(t *testing.T) {
			e := scanEnv(t, tc.known, tc.extra)
			_, body := e.do(t, "GET", "/discovery/scan/abc123", "admin", nil)
			got := scanBodyMarkup(t, body)
			path := filepath.Join("..", "..", "testdata", "scan", tc.file)
			if *updateScanFixtures {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (go test ./internal/web -run TestScanResultFixtures -update-scan-fixtures)", err)
			}
			if string(want) != got {
				t.Errorf("%s is stale: the scan result markup changed; regenerate with go test ./internal/web -run TestScanResultFixtures -update-scan-fixtures", path)
			}
		})
	}
}

// scanBodyMarkup is the <div id="scan"> of a result page, up to its matching closing tag.
func scanBodyMarkup(t *testing.T, page string) string {
	t.Helper()
	start := strings.Index(page, `<div id="scan"`)
	if start < 0 {
		t.Fatal("no #scan in the page")
	}
	depth := 0
	for i := start; i < len(page); {
		switch {
		case strings.HasPrefix(page[i:], "<div"):
			depth++
			i += 4
		case strings.HasPrefix(page[i:], "</div>"):
			depth--
			i += len("</div>")
			if depth == 0 {
				return page[start:i] + "\n"
			}
		default:
			i++
		}
	}
	t.Fatal("#scan is not closed")
	return ""
}
