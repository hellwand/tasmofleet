package discovery

import (
	"errors"
	"net/netip"
	"testing"
	"time"
)

// reusedNumber is the situation behind #745. A proposal is shown; its row is then deleted (Add,
// the overtaken cleanup or the 30-day prune all do that) and another device is announced. SQLite
// hands the new row the number of the old one, so an admin whose page still shows the old device
// holds a number that now names the new one. It returns what the admin saw and what is behind
// the number now.
func reusedNumber(t *testing.T, e *env) (shown, now Proposal) {
	t.Helper()
	first := candidate{via: ViaMQTT, mac: "02:00:5e:40:b4:34", ip: netip.MustParseAddr("172.31.13.77"), name: "shown"}
	if err := e.mgr.propose(bg, first, "new", 0, "not in the inventory"); err != nil {
		t.Fatal(err)
	}
	shown = mustProposals(t, e)[0]
	if err := e.st.DeleteProposal(bg, shown.ID); err != nil {
		t.Fatal(err)
	}
	second := candidate{via: ViaMQTT, mac: "02:00:5e:40:b4:35", ip: netip.MustParseAddr("172.31.13.78"), name: "never shown"}
	if err := e.mgr.propose(bg, second, "new", 0, "not in the inventory"); err != nil {
		t.Fatal(err)
	}
	now = mustProposals(t, e)[0]
	if now.ID != shown.ID {
		t.Fatalf("the new row has number %d, not the reused %d: the premise of #745 no longer holds, rethink these tests", now.ID, shown.ID)
	}
	return shown, now
}

// Why: the MAC is what makes a device this device. Two devices that show the same name and address
// (a replacement unit that took over the old one's lease) must still not share a version, or
// the check could not tell the two behind one reused number apart.
func TestVersionOfAnotherMACDiffersWhateverElseIsTheSame(t *testing.T) {
	e := newEnv(t, "172.31.0.0/16", nil)
	same := func(mac string) Proposal {
		if err := e.mgr.propose(bg, candidate{via: ViaMQTT, mac: mac, ip: netip.MustParseAddr("172.31.13.77"), name: "lamp"}, "new", 0, "not in the inventory"); err != nil {
			t.Fatal(err)
		}
		p := mustProposals(t, e)[0]
		if err := e.st.DeleteProposal(bg, p.ID); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if a, b := same("02:00:5e:40:b4:34"), same("02:00:5e:40:b4:35"); a.ID != b.ID || a.Version == b.Version {
		t.Fatalf("numbers %d and %d, versions equal: %v", a.ID, b.ID, a.Version == b.Version)
	}
}

// Why (#745): Ignore clicked on a page loaded earlier must not hide a device that appeared after
// the page was loaded and that the admin never saw.
func TestDismissRefusesStaleVersionAfterNumberReuse(t *testing.T) {
	e := newEnv(t, "172.31.0.0/16", nil)
	shown, now := reusedNumber(t, e)
	if err := e.mgr.Dismiss(bg, "admin", shown.ID, shown.Version); !errors.Is(err, ErrStale) {
		t.Fatalf("err = %v, want ErrStale", err)
	}
	got := mustProposals(t, e)
	if len(got) != 1 || got[0].Device.MAC != now.Device.MAC {
		t.Fatalf("the new device is no longer listed under New devices: %+v", got)
	}
	// The refusal is on the record, so a stale click can be told from a successful Ignore.
	e.audit.mu.Lock()
	defer e.audit.mu.Unlock()
	if n := len(e.audit.ev); n != 1 || e.audit.ev[0].Action != "discovery.dismiss" || e.audit.ev[0].Result == "ok" {
		t.Errorf("audit = %+v, want the refused discovery.dismiss", e.audit.ev)
	}
}

// Why (#745): an empty version cannot be checked, so it must not pass as "any": a client that
// leaves it out would be back to acting on the number alone.
func TestDismissAndUndismissNeedAVersion(t *testing.T) {
	e := newEnv(t, "172.31.0.0/16", nil)
	_, now := reusedNumber(t, e)
	if err := e.mgr.Dismiss(bg, "admin", now.ID, ""); !errors.Is(err, ErrStale) {
		t.Fatalf("dismiss without version: %v", err)
	}
	if err := e.mgr.Dismiss(bg, "admin", now.ID, now.Version); err != nil {
		t.Fatal(err)
	}
	if err := e.mgr.Undismiss(bg, "admin", now.ID, ""); !errors.Is(err, ErrStale) {
		t.Fatalf("undismiss without version: %v", err)
	}
	if ign, _ := e.mgr.Ignored(bg); len(ign) != 1 {
		t.Fatalf("the device was restored without a version: %+v", ign)
	}
}

// Why (#745): Restore on a page loaded earlier must not bring back a device that someone else
// ignored on purpose, only because it now holds the number of the one the page showed.
func TestUndismissRefusesStaleVersionAfterNumberReuse(t *testing.T) {
	e := newEnv(t, "172.31.0.0/16", nil)
	shown, now := reusedNumber(t, e)
	if err := e.mgr.Dismiss(bg, "admin", now.ID, now.Version); err != nil { // the second admin ignores the new device
		t.Fatal(err)
	}
	if err := e.mgr.Undismiss(bg, "admin", shown.ID, shown.Version); !errors.Is(err, ErrStale) {
		t.Fatalf("err = %v, want ErrStale", err)
	}
	if got := mustProposals(t, e); len(got) != 0 {
		t.Fatalf("the ignored device was restored: %+v", got)
	}
	if ign, _ := e.mgr.Ignored(bg); len(ign) != 1 || ign[0].Device.MAC != now.Device.MAC {
		t.Fatalf("the new device is not ignored any more: %+v", ign)
	}
}

// Why: the same device announcing another address after the page was loaded is a different
// proposal (Add refuses it the same way): what the admin decided on is not what is stored now.
func TestDismissRefusesVersionOfEarlierSighting(t *testing.T) {
	e := newEnv(t, "172.31.0.0/16", nil)
	e.mgr.handleMessage(bg, discoveryMsg("02005ED22C14", "172.31.13.31", "lamp"))
	shown := mustProposals(t, e)[0]
	e.mgr.last = map[string]time.Time{}
	e.mgr.handleMessage(bg, discoveryMsg("02005ED22C14", "172.31.13.99", "lamp"))
	if err := e.mgr.Dismiss(bg, "admin", shown.ID, shown.Version); !errors.Is(err, ErrStale) {
		t.Fatalf("err = %v, want ErrStale", err)
	}
	if got := mustProposals(t, e); len(got) != 1 {
		t.Fatalf("proposals = %+v", got)
	}
}
