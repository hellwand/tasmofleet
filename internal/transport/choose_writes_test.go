package transport

import (
	"strings"
	"testing"
	"time"
)

// Why (#605): a device usable on our broker under the strict rule (its own GroupTopic read, no other device's
// unread, topic exclusive, FullTopic ours, last will Online) takes changes over MQTT first, so a device HTTP
// struggles with is not loaded with an HTTP request per change. A device whose topic might be shared, or whose
// MQTT reads just went unanswered, keeps HTTP while it works; a message to a shared topic would change other
// devices too, and a write that is never answered is an unknown outcome.
func TestWritesGoOverMQTTFirstUnderTheStrictRule(t *testing.T) {
	cases := []struct {
		name  string
		mod   func(*Facts)
		write Path
		note  string // substring of the writes reason
	}{
		{"strict usable, HTTP ok: MQTT", func(f *Facts) {}, MQTT, "over MQTT first"},
		{"strict usable, HTTP unknown: MQTT", func(f *Facts) { f.HTTP = HTTPState{} }, MQTT, "over MQTT first"},
		{"strict usable, HTTP failing: MQTT, and says why", func(f *Facts) { *f = httpDown(*f) }, MQTT, "cannot reach the device over HTTP"},
		{"own GroupTopic unread, HTTP ok: HTTP, and says why", func(f *Facts) { f.MQTT.GroupTopic = "" }, HTTP, "GroupTopic has not been read recently"},
		{"own GroupTopic older than the last probe: HTTP", func(f *Facts) { f.MQTT.LastProbeAt = f.MQTT.GroupTopicReadAt.Add(time.Minute) }, HTTP, "GroupTopic"},
		{"another device's GroupTopic unread: HTTP", func(f *Facts) { f.MQTT.OthersGroupUnread = 1 }, HTTP, "1 other device's group topic has not been read yet"},
		{"MQTT reads unanswered, HTTP ok: HTTP", func(f *Facts) { f.MQTT.ReadsUnansweredSince = t0.Add(-time.Minute) }, HTTP, "got no answer"},
		{"MQTT reads unanswered, HTTP failing: MQTT", func(f *Facts) { *f = httpDown(*f); f.MQTT.ReadsUnansweredSince = t0.Add(-time.Minute) }, MQTT, "cannot reach the device over HTTP"},
		{"LWT not online: HTTP", func(f *Facts) { f.MQTT.LWTOnline = false }, HTTP, "not online on MQTT"},
		{"other broker: HTTP", func(f *Facts) { f.MQTT.Ours = false }, HTTP, "does not use TasmoFleet's MQTT broker"},
		{"shared topic: HTTP", func(f *Facts) { f.MQTT.TopicExclusive = false }, HTTP, "another device listens"},
		{"bus down: HTTP", func(f *Facts) { f.MQTT.BusConnected = false }, HTTP, "not connected to its MQTT broker"},
		// Why: both ways fail: nothing is sent, and the sentence names both causes.
		{"GroupTopic unread, HTTP failing: none", func(f *Facts) { *f = httpDown(*f); f.MQTT.GroupTopic = "" }, None, "no path"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := base()
			c.mod(&f)
			p := Choose(f)
			if p.Writes != c.write {
				t.Fatalf("writes=%s, want %s\nreasons: %v", p.Writes, c.write, p.Reasons)
			}
			if got := p.Reason(ActWrites); !strings.Contains(got, c.note) {
				t.Errorf("writes reason %q lacks %q", got, c.note)
			}
		})
	}
}

// Why (#605): the connection card shows this sentence under "Console and Settings: changing". It must say what
// the router does: MQTT first, HTTP only when nothing was published, and which changes keep HTTP first (a change
// of the device's MQTT connection, a password or Wi-Fi name, a GPIO template, and a console or bulk command whose
// arguments the audit hides: Router.write keeps those on HTTP first while HTTP works, so a card that only says
// "MQTT first" would contradict the audit's via=http for an everyday `Dimmer 50`); and when HTTP is used, that the
// switches use it too and why.
func TestWritesReasonSaysWhatTheRouterDoes(t *testing.T) {
	got := Choose(base()).Reason(ActWrites)
	for _, want := range []string{"over MQTT first", "over HTTP if nothing could be published", "MQTT connection", "password or Wi-Fi name", "GPIO template", "console or bulk command", "audit log hides"} {
		if !strings.Contains(got, want) {
			t.Errorf("reason %q lacks %q", got, want)
		}
	}
	f := base()
	f.MQTT.GroupTopic = ""
	got = Choose(f).Reason(ActWrites)
	for _, want := range []string{"over the device's HTTP API", "switches", "refresh the device's identity"} {
		if !strings.Contains(got, want) {
			t.Errorf("HTTP reason %q lacks %q", got, want)
		}
	}
}
