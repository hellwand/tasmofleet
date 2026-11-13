package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/hellwand/tasmofleet/internal/inventory"
	"github.com/hellwand/tasmofleet/internal/model"
	"github.com/hellwand/tasmofleet/internal/mqttbus"
	"github.com/hellwand/tasmofleet/internal/store"
)

// logBuf collects log lines so a test can count them.
type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuf) lines() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := strings.TrimSpace(l.b.String())
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func loggerAt(level slog.Level) (*slog.Logger, *logBuf) {
	buf := &logBuf{}
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: level})), buf
}

// Why (#715): a device whose retained discovery config is unusable never shows up on "Add devices",
// and at the default log level (info) nothing said why. The operator needs the device (the MAC
// level of the topic) and the reason. The payload is attacker-controlled text and must never reach
// the log. A retained config is replayed at every broker reconnect, so the same refusal is logged
// once, not once per reconnect; a different reason, or a config that works in between, is news again.
func TestRefusedConfigIsLoggedOnceAtInfo(t *testing.T) {
	log, buf := loggerAt(slog.LevelInfo)
	e := newEnv(t, "172.31.0.0/16", func(o *Options) { o.Logger = log })
	const marker = "PAYLOAD-MARKER-do-not-log"
	noIP := mqttbus.Message{Kind: mqttbus.KindDiscovery, Topic: "02005ED22C14", Retained: true,
		Payload: []byte(`{"mac":"02005ED22C14","dn":"` + marker + `","t":"lamp"}`)}

	e.mgr.handleMessage(bg, noIP)
	got := buf.lines()
	if len(got) != 1 {
		t.Fatalf("first refusal wrote %d info lines, want 1: %q", len(got), got)
	}
	for _, want := range []string{"02:00:5e:d2:2c:14", "no valid IP address"} {
		if !strings.Contains(got[0], want) {
			t.Errorf("line %q lacks %q", got[0], want)
		}
	}
	if strings.Contains(got[0], marker) {
		t.Errorf("the payload reached the log: %q", got[0])
	}

	for range 3 { // broker reconnects replay the retained message
		e.mgr.handleMessage(bg, noIP)
	}
	if n := len(buf.lines()); n != 1 {
		t.Fatalf("replays wrote %d lines in total, want still 1", n)
	}

	notJSON := mqttbus.Message{Kind: mqttbus.KindDiscovery, Topic: "02005ED22C14", Payload: []byte(marker)}
	e.mgr.handleMessage(bg, notJSON)
	if got = buf.lines(); len(got) != 2 || !strings.Contains(got[1], "not JSON") || strings.Contains(got[1], marker) {
		t.Fatalf("a new reason must be logged once more, without the payload: %q", got)
	}

	// An empty payload withdraws the retained config (Tasmota does so when discovery is switched off);
	// that is no refusal and writes nothing at info, and it does not re-arm the log either.
	e.mgr.handleMessage(bg, mqttbus.Message{Kind: mqttbus.KindDiscovery, Topic: "02005ED22C14", Retained: true})
	if n := len(buf.lines()); n != 2 {
		t.Fatalf("a withdrawn config changed the log to %d lines, want 2", n)
	}
	e.mgr.handleMessage(bg, noIP)
	if n := len(buf.lines()); n != 2 {
		t.Fatalf("a withdrawal re-armed the log: %d lines, want 2", n)
	}

	// Fixed and broken again later: the second breakage is news.
	e.mgr.handleMessage(bg, discoveryMsg("02005ED22C14", "172.31.13.31", "lamp"))
	if n := len(buf.lines()); n != 2 {
		t.Fatalf("a good config wrote a line: %q", buf.lines())
	}
	e.mgr.handleMessage(bg, noIP)
	if n := len(buf.lines()); n != 3 {
		t.Fatalf("a refusal after a working config wrote %d lines in total, want 3", n)
	}
}

// Why (#715): the rate limit allows one evaluation per MAC per 15 s precisely so that a publisher on
// the broker cannot turn one message into a stream of work; the refusal log must not become that
// stream. Whoever can publish can alternate two broken payloads, or a working config and a broken one,
// for one MAC. Each reason is news once, and a working config re-arms the log at most once per gap.
func TestRefusalLogCannotBeFloodedByAlternatingPayloads(t *testing.T) {
	const mac = "02005ED22C14"
	noIP := mqttbus.Message{Kind: mqttbus.KindDiscovery, Topic: mac, Payload: []byte(`{"mac":"` + mac + `","t":"lamp"}`)}
	notJSON := mqttbus.Message{Kind: mqttbus.KindDiscovery, Topic: mac, Payload: []byte("nope")}
	noMAC := mqttbus.Message{Kind: mqttbus.KindDiscovery, Topic: mac, Payload: []byte(`{"ip":"172.31.13.31"}`)}

	t.Run("two bad reasons alternating", func(t *testing.T) {
		log, buf := loggerAt(slog.LevelInfo)
		e := newEnv(t, "172.31.0.0/16", func(o *Options) { o.Logger = log })
		for range 50 {
			e.mgr.handleMessage(bg, noIP)
			e.mgr.handleMessage(bg, notJSON)
		}
		if n := len(buf.lines()); n != 2 {
			t.Fatalf("100 alternating refusals wrote %d info lines, want exactly 2 (one per reason): %q", n, buf.lines())
		}
		e.mgr.handleMessage(bg, noMAC)
		if n := len(buf.lines()); n != 3 {
			t.Fatalf("a third reason wrote %d lines in total, want 3", n)
		}
	})

	t.Run("working and broken config alternating", func(t *testing.T) {
		log, buf := loggerAt(slog.LevelInfo)
		e := newEnv(t, "172.31.0.0/16", func(o *Options) { o.Logger = log })
		for range 50 {
			e.mgr.handleMessage(bg, discoveryMsg(mac, "172.31.13.31", "lamp"))
			e.mgr.handleMessage(bg, noIP)
		}
		if n := len(buf.lines()); n != 1 {
			t.Fatalf("100 alternating messages within one gap wrote %d info lines, want exactly 1: %q", n, buf.lines())
		}
	})
}

// Why (#715): the table that remembers what was logged is filled by whoever publishes on the broker.
// Like the rate-limit table it must not grow without bound and fails closed: past its cap a new MAC
// gets no info line (the debug line stays) and nothing already known is forgotten, so a flood of
// made-up MACs cannot flood the log either.
func TestRefusedConfigLogIsBounded(t *testing.T) {
	log, buf := loggerAt(slog.LevelInfo)
	e := newEnv(t, "172.31.0.0/16", func(o *Options) { o.Logger = log })
	const flood = 6000 // more than any table cap; real fleets have dozens of devices
	send := func(i int) {
		topic := fmt.Sprintf("0200%08X", i)
		e.mgr.handleMessage(bg, mqttbus.Message{Kind: mqttbus.KindDiscovery, Topic: topic, Payload: []byte("nope")})
	}
	for i := 0; i < flood; i++ {
		send(i)
	}
	n := len(buf.lines())
	if n == 0 || n >= flood {
		t.Fatalf("%d info lines for %d refused MACs: want some, but bounded below the flood", n, flood)
	}
	send(0) // the first MAC was logged and is remembered
	send(flood - 1)
	if after := len(buf.lines()); after != n {
		t.Fatalf("replays after the flood wrote %d more lines", after-n)
	}
}

// Why (#715): a message dropped because the queue is full is a lost device announcement. It must be
// countable (metric tasmofleet_discovery_proposals_dropped_total), and HandleMQTT must still return at once.
func TestFullQueueDropIsCounted(t *testing.T) {
	var mu sync.Mutex
	var dropped []string
	e := newEnv(t, "172.31.0.0/16", func(o *Options) {
		o.OnDropped = func(kind string) { mu.Lock(); dropped = append(dropped, kind); mu.Unlock() }
	})
	for i := 0; i < maxQueuedMessages; i++ {
		e.mgr.HandleMQTT(mqttbus.Message{Kind: mqttbus.KindLWT, Topic: fmt.Sprintf("t%d", i), Payload: []byte("Online")})
	}
	e.mgr.HandleMQTT(mqttbus.Message{Kind: mqttbus.KindLWT, Topic: "t0", Payload: []byte("Offline")}) // coalesced into a queued slot
	mu.Lock()
	n := len(dropped)
	mu.Unlock()
	if n != 0 {
		t.Fatalf("%d drops before the queue was full", n)
	}
	e.mgr.HandleMQTT(mqttbus.Message{Kind: mqttbus.KindLWT, Topic: "one-too-many", Payload: []byte("Online")})
	mu.Lock()
	defer mu.Unlock()
	if len(dropped) != 1 || dropped[0] != "queue_full" {
		t.Fatalf("drops = %v, want one queue_full", dropped)
	}
}

// conflictingInventory answers every UpsertExisting like an inventory whose record changed meanwhile.
type conflictingInventory struct{ Inventory }

func (conflictingInventory) UpsertExisting(context.Context, string, model.DeviceID, model.Device) (model.DeviceID, bool, error) {
	return 0, false, inventory.ErrConflict
}

// Why (#715): when the stored device changed between the lookup and the write, the claim is dropped
// ("the next sighting decides"). That is correct, but a missing address update must be traceable to
// a MAC in the debug log.
func TestMoveConflictIsLoggedAtDebug(t *testing.T) {
	log, buf := loggerAt(slog.LevelDebug)
	e := newEnv(t, "172.31.0.0/16", func(o *Options) { o.Logger = log })
	e.inv.Create(bg, "t", model.Device{Name: "lamp", Address: "172.31.13.31", MAC: "02:00:5e:d2:2c:14", Topic: "lamp"}) //nolint:errcheck // setup
	e.mgr.o.Inventory = conflictingInventory{Inventory: e.mgr.o.Inventory}
	e.mgr.handleMessage(bg, discoveryMsg("02005ED22C14", "172.31.13.77", "lamp"))
	var found bool
	for _, l := range buf.lines() {
		found = found || strings.Contains(l, "02:00:5e:d2:2c:14") && strings.Contains(l, "changed")
	}
	if !found {
		t.Fatalf("no debug line names the MAC and the conflict: %q", buf.lines())
	}
}

// failingLookup is a store whose row lookup fails.
type failingLookup struct{ store.DiscoveryStore }

func (failingLookup) GetProposal(context.Context, int64) (store.ProposalRow, error) {
	return store.ProposalRow{}, errors.New("database is busy")
}

// Why (#716): the audit log must say which device was ignored or restored. "proposal:7" with an empty
// detail only gave an id that is gone after 30 days. The detail carries what Accept records (kind,
// MAC, address) and the name, which is what a person recognises.
func TestDismissAndUndismissAuditTheDevice(t *testing.T) {
	last := func(e *env) (model.AuditEvent, map[string]any) {
		t.Helper()
		e.audit.mu.Lock()
		defer e.audit.mu.Unlock()
		ev := e.audit.ev[len(e.audit.ev)-1]
		var d map[string]any
		if err := json.Unmarshal([]byte(ev.Detail), &d); err != nil {
			t.Fatalf("detail %q: %v", ev.Detail, err)
		}
		return ev, d
	}
	t.Run("detail names the device", func(t *testing.T) {
		e := newEnv(t, "172.31.0.0/16", nil)
		e.mgr.handleMessage(bg, discoveryMsg("02005ED22C14", "172.31.13.31", "lamp"))
		shown := mustProposals(t, e)[0]
		id := shown.ID
		want := map[string]any{"kind": KindNew, "name": "Lamp lamp", "mac": "02:00:5e:d2:2c:14", "address": "172.31.13.31"}
		for _, step := range []struct {
			action string
			do     func() error
		}{
			{"discovery.dismiss", func() error { return e.mgr.Dismiss(bg, "admin", id, shown.Version) }},
			{"discovery.undismiss", func() error { return e.mgr.Undismiss(bg, "admin", id, shown.Version) }},
		} {
			if err := step.do(); err != nil {
				t.Fatal(err)
			}
			ev, d := last(e)
			if ev.Action != step.action || ev.Target != fmt.Sprintf("proposal:%d", id) {
				t.Fatalf("event %+v", ev)
			}
			for k, v := range want {
				if d[k] != v {
					t.Errorf("%s: detail %s = %v, want %v (detail %s)", step.action, k, d[k], v, ev.Detail)
				}
			}
		}
	})
	// The lookup is what the version is checked against (#745): without it nothing can tell whether the
	// number still names the device the admin saw, so an Ignore is refused rather than guessed.
	t.Run("a failed lookup refuses the ignore", func(t *testing.T) {
		e := newEnv(t, "172.31.0.0/16", nil)
		e.mgr.handleMessage(bg, discoveryMsg("02005ED22C14", "172.31.13.31", "lamp"))
		shown := mustProposals(t, e)[0]
		e.mgr.o.Store = failingLookup{DiscoveryStore: e.mgr.o.Store}
		if err := e.mgr.Dismiss(bg, "admin", shown.ID, shown.Version); err == nil {
			t.Fatal("dismiss went through without a lookup")
		}
		if ev, _ := last(e); ev.Action != "discovery.dismiss" || ev.Result == "ok" {
			t.Fatalf("event %+v", ev)
		}
		e.mgr.o.Store = e.st
		if len(mustProposals(t, e)) != 1 {
			t.Fatal("the proposal was hidden although the ignore was refused")
		}
	})
	t.Run("an unknown id is still refused and audited", func(t *testing.T) {
		e := newEnv(t, "172.31.0.0/16", nil)
		if err := e.mgr.Dismiss(bg, "admin", 999, "v"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v", err)
		}
		if ev, _ := last(e); ev.Result != ErrNotFound.Error() {
			t.Fatalf("event %+v", ev)
		}
	})
}
