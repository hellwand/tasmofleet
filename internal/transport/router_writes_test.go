package transport

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/hellwand/tasmofleet/internal/model"
	"github.com/hellwand/tasmofleet/internal/mqttcmd"
	"github.com/hellwand/tasmofleet/internal/tasmota"
)

// Why (#605): a device usable on our broker under the strict rule takes changes over MQTT first, while HTTP
// works too: console and settings writes, Restart, a Backlog. Exactly one send, and the trace (the audit's
// "via") names MQTT.
func TestStrictlyUsableWritesGoOverMQTTFirst(t *testing.T) {
	for _, c := range []string{"Power1 ON", "Restart 1", "TelePeriod 60", "Backlog SetOption19 1; SaveData 1"} {
		g := newRig(t)
		ctx, tr := WithTrace(t.Context())
		raw, err := g.r.Command(ctx, g.target(), c)
		if err != nil || string(raw) != `{"MQTT":"yes"}` || g.mq.n() != 1 || g.http.n() != 0 || tr.Via() != "mqtt" {
			t.Errorf("%q: raw=%s err=%v mqtt=%d http=%d via=%s", c, raw, err, g.mq.n(), g.http.n(), tr.Via())
		}
	}
}

// Why (#605, D4): a write MQTT provably did not publish (the channel refused it: an executor, a file command,
// Backlog2; the bus went down; the device's last will said Offline) changed nothing, so it is sent once over
// HTTP. Never back to MQTT within the call, also when HTTP did not send either.
func TestAWriteMQTTDidNotPublishIsSentOnceOverHTTP(t *testing.T) {
	for _, mqttErr := range []error{
		fmt.Errorf("%w: Rule1 can run other commands", mqttcmd.ErrRefused),
		fmt.Errorf("%w: not connected", tasmota.ErrUnreachable),
		fmt.Errorf("%w: the device reported itself offline (LWT), nothing was sent", tasmota.ErrUnreachable),
	} {
		g := newRig(t)
		g.mq.err = mqttErr
		ctx, tr := WithTrace(t.Context())
		raw, err := g.r.Command(ctx, g.target(), "Rule1 1")
		if err != nil || string(raw) != `{"HTTP":"yes"}` || g.mq.n() != 1 || g.http.n() != 1 || tr.Via() != "http" {
			t.Errorf("%v: raw=%s err=%v mqtt=%d http=%d via=%s", mqttErr, raw, err, g.mq.n(), g.http.n(), tr.Via())
		}
		g.http.err = tasmota.ErrForbiddenAddr // HTTP did not send either: no second MQTT attempt
		if _, err := g.r.Command(t.Context(), g.target(), "Rule1 1"); !errors.Is(err, tasmota.ErrForbiddenAddr) || g.mq.n() != 2 || g.http.n() != 2 {
			t.Errorf("%v, HTTP not sent: err=%v mqtt=%d http=%d", mqttErr, err, g.mq.n(), g.http.n())
		}
	}
}

// Why (#605, D4): a write that MQTT published and the device did not confirm may have run. A second send over
// HTTP could switch a relay twice or restart the device twice, so the caller gets the unknown outcome
// (tasmota.ErrTimeout), also while HTTP works, and also when the caller gave up waiting.
func TestATimedOutMQTTWriteIsNeverResent(t *testing.T) {
	for _, mqttErr := range []error{
		fmt.Errorf("mqtt: no answer to Power1: %w", tasmota.ErrTimeout),
		fmt.Errorf("mqtt: the device went offline before it answered Restart (outcome unknown): %w", tasmota.ErrTimeout),
		fmt.Errorf("mqtt: Power1 was sent and not answered before the caller gave up (outcome unknown): %w: %w", tasmota.ErrTimeout, context.Canceled),
	} {
		g := newRig(t)
		g.mq.err = mqttErr
		ctx, tr := WithTrace(t.Context())
		_, err := g.r.Command(ctx, g.target(), "Power1 TOGGLE")
		if !errors.Is(err, tasmota.ErrTimeout) || g.mq.n() != 1 || g.http.n() != 0 || tr.Via() != "mqtt" {
			t.Errorf("%v: err=%v mqtt=%d http=%d via=%s", mqttErr, err, g.mq.n(), g.http.n(), tr.Via())
		}
	}
}

// Why (#605, D13): a change of the device's MQTT connection (Topic, broker, login, SetOption3 0) published over
// MQTT can cut TasmoFleet off from the device. While HTTP works it goes over HTTP, with no question asked, and
// HTTP's answer confirms it; an ordinary write to the same device still goes over MQTT first.
func TestACutOffWriteGoesOverHTTPWhileHTTPWorks(t *testing.T) {
	g := newRig(t)
	for _, c := range []string{
		"Topic other", "Backlog Power ON; MqttHost 10.0.0.9", "SetOption3 0", "FullTopic %prefix%/x/", "MqttUser x", "MqttPassword x",
	} {
		before := g.http.n()
		ctx, tr := WithTrace(t.Context())
		if _, err := g.r.Command(ctx, g.target(), c); err != nil || g.http.n() != before+1 || g.mq.n() != 0 || tr.Via() != "http" {
			t.Fatalf("%q: err=%v http=%d mqtt=%d via=%s", c, err, g.http.n(), g.mq.n(), tr.Via())
		}
	}
	if _, err := g.r.Command(t.Context(), g.target(), "Power1 ON"); err != nil || g.mq.n() != 1 {
		t.Fatalf("an ordinary write: err=%v mqtt=%d", err, g.mq.n())
	}
}

// Why (#605): a Template with a template restarts the device and was never tried over MQTT on a real one, so in
// 0.37.0 it stays HTTP first wherever it comes from (the GPIO template job, the console), also inside a Backlog.
// The bare Template is a read and an ordinary write is a write: both go over MQTT first.
func TestTemplateWritesStayHTTPFirst(t *testing.T) {
	g := newRig(t)
	for _, c := range []string{
		`Template {"NAME":"Plug","GPIO":[0,0,0,0,0,0,0,0,0,0,0,0,0,0],"FLAG":0,"BASE":18}`,
		`template {"NAME":"Plug"}`,
		`Template{"NAME":"Plug"}`,
		`Backlog Template {"NAME":"Plug","GPIO":[0],"FLAG":0,"BASE":18}; Module 0`,
	} {
		before := g.http.n()
		if _, err := g.r.Command(WithConsole(t.Context()), g.target(), c); err != nil || g.http.n() != before+1 || g.mq.n() != 0 {
			t.Fatalf("%q: err=%v http=%d mqtt=%d", c, err, g.http.n(), g.mq.n())
		}
	}
	if _, err := g.r.Command(t.Context(), g.target(), "Template"); err != nil || g.mq.n() != 1 {
		t.Fatalf("the bare Template (a read): err=%v mqtt=%d", err, g.mq.n())
	}
	if _, err := g.r.Command(t.Context(), g.target(), "Module 0"); err != nil || g.mq.n() != 2 {
		t.Fatalf("an ordinary write: err=%v mqtt=%d", err, g.mq.n())
	}
	// HTTP first is the old order, failover included: HTTP that provably sent nothing still lets MQTT carry it.
	g.http.err = tasmota.ErrForbiddenAddr
	if _, err := g.r.Command(t.Context(), g.target(), `Template {"NAME":"Plug"}`); err != nil || g.mq.n() != 3 {
		t.Fatalf("after HTTP sent nothing: err=%v mqtt=%d", err, g.mq.n())
	}
}

// Why (#605 review): a write whose arguments are secret would cross the broker in clear text over MQTT, readable by
// every client allowed to subscribe to cmnd/#. Secret means a name cmdpolicy keeps secret (a password, a Wi-Fi name,
// a token, the range extender's RgxSSId and RgxPassword, a BLE bind key, the Zigbee network key, WiFiTest's login)
// or an argument that carries a credential (userinfo in a URL, a token or an empty user included, Sendmail's SMTP
// login, WebSend's [host,user:pass], password=), alone or inside any Backlog form, whatever the case or the index
// spelling. Before #605 such a write crossed the broker only while
// HTTP failed, so it keeps HTTP first while HTTP works; with HTTP failing, MQTT carries it as before and its reply is
// masked. An ordinary write, an OtaUrl or a WebSend without a login among them, still goes MQTT first.
func TestSecretWritesStayHTTPFirstWhileHTTPWorks(t *testing.T) {
	named := []string{ // cmdpolicy redacts them: over MQTT their reply is masked too
		"WebPassword x", "Backlog TelePeriod 60; Password1 x", "SSId1 home", "TmToken abc",
		"RgxPassword x", "RgxSSId net", "Backlog TelePeriod 60; RgxPassword x",
		"Backlog0 Power 1; WebPassword x", "Backlog3 Power 1; RgxPassword x", "rGxPaSsWoRd x", "Password01 x",
		"WebPassword2 x", "Password3 x", "Backlog Power1 ON; WebPassword x",
		// keys and test credentials that went MQTT first in an earlier round of #605, where 0.36.0 used HTTP
		"MI32Key 00112233445566778899AABBCCDDEEFFA4C138AABBCC", "MI32Keys1 A4C138AABBCC=00112233445566778899AABBCCDDEEFF",
		`ZbConfig {"Channel":11,"PanID":"0x1A63","KeyL":"0x0F0D0B0907050301L","KeyH":"0x0D0C0A0806040200L"}`,
		"Backlog Power 1; MI32Key 00112233445566778899AABBCCDDEEFFA4C138AABBCC", "mi32keys A4C138AABBCC 00112233445566778899AABBCCDDEEFF",
		"WiFiTest3 MyNet+hunter2",
	}
	secrets := append(named, // a credential in the text of a command whose name is not secret
		"OtaUrl http://u:p@h/fw.bin", "Backlog Power 1; OtaUrl http://u:p@h/f",
		"Sendmail [smtp.x:465:u:pw:a@b:c@d:s] body", "WebSend [h,u:pw] Power1 ON", "OTAURL HTTP://U:P@H/F",
		"OtaUrl https://tok@h/fw.bin", "OtaUrl http://:p@h/fw.bin",
	)
	g := newRig(t)
	for _, c := range secrets {
		before := g.http.n()
		ctx, tr := WithTrace(t.Context())
		if _, err := g.r.Command(ctx, g.target(), c); err != nil || g.http.n() != before+1 || g.mq.n() != 0 || tr.Via() != "http" {
			t.Fatalf("%q, HTTP works: err=%v http=%d mqtt=%d via=%s", c, err, g.http.n(), g.mq.n(), tr.Via())
		}
	}
	for i, c := range []string{"Backlog TelePeriod 60; SaveData 1", "OtaUrl http://h/fw.bin", "WebSend [h:80] Power1 ON"} {
		ctx, tr := WithTrace(t.Context())
		if _, err := g.r.Command(ctx, g.target(), c); err != nil || g.mq.n() != 1+i || tr.Via() != "mqtt" {
			t.Fatalf("%q, an ordinary write: err=%v mqtt=%d via=%s", c, err, g.mq.n(), tr.Via())
		}
	}
	g.mem.Fail(7, HTTPFailUnreachable) // HTTP failing: MQTT carries the secret write, as before #605
	g.mq.reply = `{"WebPassword":"x","Password1":"x"}`
	for i, c := range secrets {
		httpBefore := g.http.n()
		raw, err := g.r.Command(t.Context(), g.target(), c)
		if err != nil || g.mq.n() != 4+i || g.http.n() != httpBefore || i < len(named) && strings.Contains(string(raw), `"x"`) {
			t.Fatalf("%q, HTTP failing: raw=%s err=%v mqtt=%d http=%d", c, raw, err, g.mq.n(), g.http.n())
		}
	}
}

// Why (#605 review, T11): what a person types in the console or sends as a bulk command can hide a secret in any
// command, and four rounds of secret names each missed one (a password inside If, a BLE pairing PIN, Sendmail's
// login without an '@'). So the console's writes are judged by the audit's own allowlist (cmdpolicy.ShownInFull):
// one whose logged form hides or masks anything keeps HTTP first while HTTP works, as in 0.36.0, and crosses the
// broker in clear text only while HTTP fails. One the audit shows in full goes MQTT first. Programmatic writes (the
// settings page, power, Restart) keep the curated rule: the same text without the console marker goes MQTT first.
func TestConsoleWritesTheAuditHidesStayHTTPFirstWhileHTTPWorks(t *testing.T) {
	hidden := []string{
		"If (1==1) WebPassword hunter2", "If (1==1) Status 0",
		"Backlog Power 1; If (1==1) MI32Key 00112233445566778899AABBCCDDEEFFA4C138AABBCC",
		"BLEPair A4C138AABBCC 123456", "Sendmail [smtp.example.com:587:alice:hunter2:*:bob:Hi] body",
		"Backlog Power 1; Sendmail [smtp.example.com:587:alice:hunter2:*:bob:Hi] b", "PowerDelta 10",
	}
	g := newRig(t)
	for _, c := range hidden {
		before := g.http.n()
		ctx, tr := WithTrace(WithConsole(t.Context()))
		if _, err := g.r.Command(ctx, g.target(), c); err != nil || g.http.n() != before+1 || g.mq.n() != 0 || tr.Via() != "http" {
			t.Fatalf("%q, console, HTTP works: err=%v http=%d mqtt=%d via=%s", c, err, g.http.n(), g.mq.n(), tr.Via())
		}
	}
	shown := []string{"TelePeriod 60", "Power1 ON", "SetOption19 1", "Backlog PowerOnState 2; SaveData 1", "Restart 1", "Backlog Power 1;SaveData 1"}
	for i, c := range shown {
		ctx, tr := WithTrace(WithConsole(t.Context()))
		if _, err := g.r.Command(ctx, g.target(), c); err != nil || g.mq.n() != 1+i || tr.Via() != "mqtt" {
			t.Fatalf("%q, console: err=%v mqtt=%d via=%s", c, err, g.mq.n(), tr.Via())
		}
	}
	httpBefore := g.http.n()
	if _, err := g.r.Command(t.Context(), g.target(), "PowerDelta 10"); err != nil || g.mq.n() != len(shown)+1 || g.http.n() != httpBefore {
		t.Fatalf("the settings page's write: err=%v mqtt=%d http=%d", err, g.mq.n(), g.http.n())
	}
	g.mem.Fail(7, HTTPFailUnreachable) // HTTP failing: MQTT carries it, as before #605
	if _, err := g.r.Command(WithConsole(t.Context()), g.target(), "BLEPair A4C138AABBCC 123456"); err != nil || g.mq.n() != len(shown)+2 || g.http.n() != httpBefore {
		t.Fatalf("console, HTTP failing: err=%v mqtt=%d http=%d", err, g.mq.n(), g.http.n())
	}
}

// Why (#605 security review): a credential in the text of a command whose name is not secret keeps the write HTTP
// first, but its reply over MQTT is returned as the device sent it, as before. The OTA engine sets OtaUrl and checks
// that the device stored exactly the URL it sent (ota.deviceAPI.SetOtaURL); a masked echo of a URL with a login
// would fail every update and every put-back of such a URL that goes over MQTT (a pinned job, HTTP failing). The
// console masks what it shows on its own (devicecmd).
func TestACredentialInTheTextLeavesTheReplyAsSent(t *testing.T) {
	const url = "http://u:p@h/fw.bin"
	g := newRig(t)
	g.mq.reply = `{"OtaUrl":"` + url + `"}`
	raw, err := g.r.Command(WithPath(t.Context(), MQTT), g.target(), "OtaUrl "+url)
	if err != nil || g.mq.n() != 1 || !strings.Contains(string(raw), url) {
		t.Fatalf("pinned to MQTT: raw=%s err=%v mqtt=%d", raw, err, g.mq.n())
	}
	g.mem.Fail(7, HTTPFailUnreachable)
	raw, err = g.r.Command(t.Context(), g.target(), "OtaUrl "+url)
	if err != nil || g.mq.n() != 2 || !strings.Contains(string(raw), url) {
		t.Fatalf("HTTP failing: raw=%s err=%v mqtt=%d", raw, err, g.mq.n())
	}
}

// Why (#605, interim until #606): Make compliant keeps today's order (HTTP unless it fails) through the marker
// app/policyfix.go sets; it moves to MQTT first in its own change. Without the marker the same write goes MQTT first.
func TestHTTPFirstWritesMarkerKeepsTodaysOrder(t *testing.T) {
	g := newRig(t)
	ctx := WithHTTPFirstWrites(t.Context())
	if _, err := g.r.Command(ctx, g.target(), "Backlog PowerOnState 2"); err != nil || g.http.n() != 1 || g.mq.n() != 0 {
		t.Fatalf("marked: err=%v http=%d mqtt=%d", err, g.http.n(), g.mq.n())
	}
	if _, err := g.r.Command(ctx, g.target(), "PowerOnState"); err != nil || g.mq.n() != 1 {
		t.Fatalf("a read under the marker still goes MQTT first: err=%v mqtt=%d", err, g.mq.n())
	}
	if _, err := g.r.Command(t.Context(), g.target(), "Backlog PowerOnState 2"); err != nil || g.http.n() != 1 || g.mq.n() != 2 {
		t.Fatalf("unmarked: err=%v http=%d mqtt=%d", err, g.http.n(), g.mq.n())
	}
	g.mem.Fail(7, HTTPFailUnreachable) // HTTP failing: MQTT carries it, as before
	if _, err := g.r.Command(ctx, g.target(), "Backlog PowerOnState 2"); err != nil || g.http.n() != 1 || g.mq.n() != 3 {
		t.Fatalf("marked, HTTP failing: err=%v http=%d mqtt=%d", err, g.http.n(), g.mq.n())
	}
}

// Why (#605, review #279): a write to a topic that might be shared would change several devices, and a device
// whose MQTT reads go unanswered would leave every write an unknown outcome. Both keep HTTP while it works.
func TestWritesThatMQTTMustNotCarryFirstGoOverHTTP(t *testing.T) {
	for _, tc := range []struct {
		name string
		mod  func(g *rig)
	}{
		{"own GroupTopic unread", func(g *rig) { g.f.groupKnown = false }},
		{"another device's GroupTopic unread", func(g *rig) { g.f.othersUnrd = 1 }},
		{"MQTT reads unanswered", func(g *rig) { g.r.unanswered.Fail(7, HTTPFailUnreachable) }},
		{"last will Offline", func(g *rig) { g.f.lwtOffline = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newRig(t)
			tc.mod(g)
			ctx, tr := WithTrace(t.Context())
			if _, err := g.r.Command(ctx, g.target(), "Power1 ON"); err != nil || g.http.n() != 1 || g.mq.n() != 0 || tr.Via() != "http" {
				t.Fatalf("err=%v http=%d mqtt=%d via=%s", err, g.http.n(), g.mq.n(), tr.Via())
			}
		})
	}
}

// Why: the device page shows Choose's writes path as what a change will do; the router must do exactly that for
// the same facts, or the card promises MQTT while the change goes over HTTP (or the other way round).
func TestRouterWritesFollowChoose(t *testing.T) {
	for _, tc := range []struct {
		name string
		mod  func(g *rig)
	}{
		{"strict usable", func(*rig) {}},
		{"own GroupTopic unread", func(g *rig) { g.f.groupKnown = false }},
		{"others unread", func(g *rig) { g.f.othersUnrd = 2 }},
		{"reads unanswered", func(g *rig) { g.r.unanswered.Fail(7, HTTPFailUnreachable) }},
		{"offline on MQTT", func(g *rig) { g.f.lwtOffline = true }},
		{"HTTP failing", func(g *rig) { g.mem.Fail(7, HTTPFailUnreachable) }},
		{"HTTP failing, reads unanswered", func(g *rig) { g.mem.Fail(7, HTTPFailUnreachable); g.r.unanswered.Fail(7, HTTPFailUnreachable) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newRig(t)
			tc.mod(g)
			f, err := g.f.FactsFor(t.Context(), g.devs.d)
			if err != nil {
				t.Fatal(err)
			}
			want := Choose(f).Writes
			if _, err := g.r.Command(t.Context(), g.target(), "TelePeriod 60"); err != nil {
				t.Fatal(err)
			}
			got := HTTP
			if g.mq.n() > 0 {
				got = MQTT
			}
			if got != want || g.mq.n()+g.http.n() != 1 {
				t.Fatalf("router used %s (mqtt=%d http=%d), Choose says %s", got, g.mq.n(), g.http.n(), want)
			}
		})
	}
}

// Why: the MQTT-first write goes through the same send as every other MQTT call, so the topic is checked
// again right before the publish (T6): a topic changed since the snapshot publishes nothing, and the write
// is then sent over HTTP (nothing was published).
func TestMQTTFirstWriteRechecksTheTopicBeforeThePublish(t *testing.T) {
	g := newRig(t)
	g.r.o.Facts = changingFacts{inner: g.f, devs: g.devs}
	if _, err := g.r.Command(t.Context(), g.target(), "Power1 ON"); err != nil || g.mq.n() != 0 || g.http.n() != 1 {
		t.Fatalf("err=%v mqtt=%d http=%d", err, g.mq.n(), g.http.n())
	}
}

// Why: without facts the router cannot check that the topic is the device's own; a write then takes the
// established HTTP path and publishes nothing (fail closed). A device that is not on our broker is changed over
// HTTP without the inventory being listed for its facts (every switch of the device list would pay for it).
func TestWritesWithoutFactsOrOffOurBrokerGoOverHTTP(t *testing.T) {
	g := newRig(t)
	g.r.o.Facts = brokenFacts{g.f}
	if _, err := g.r.Command(t.Context(), g.target(), "Power1 ON"); err != nil || g.mq.n() != 0 || g.http.n() != 1 {
		t.Fatalf("without facts: err=%v mqtt=%d http=%d", err, g.mq.n(), g.http.n())
	}
	for _, mod := range []func(*rig){
		func(g *rig) { g.devs.d.MQTT = model.MQTTOtherBroker },
		func(g *rig) { g.devs.d.MQTT = model.MQTTNone },
		func(g *rig) { g.devs.d.Topic = "" },
	} {
		g := newRig(t)
		mod(g)
		if _, err := g.r.Command(t.Context(), g.target(), "Power1 ON"); err != nil || g.mq.n() != 0 || g.http.n() != 1 || g.f.snapshotted != 0 {
			t.Fatalf("%+v: err=%v mqtt=%d http=%d facts asked %d times", g.devs.d, err, g.mq.n(), g.http.n(), g.f.snapshotted)
		}
	}
}

// Why (#605): the power toggle calls the command channel itself and asks this whether it may switch over HTTP
// after MQTT. Only a proof that nothing was published counts; a timeout (an unknown outcome) never does.
func TestNotPublishedCountsOnlyProofsThatNothingWasSent(t *testing.T) {
	for err, want := range map[error]bool{
		nil: false,
		fmt.Errorf("%w: refused", mqttcmd.ErrRefused):                                               true,
		fmt.Errorf("%w: not connected", tasmota.ErrUnreachable):                                     true,
		fmt.Errorf("mqtt: no answer: %w", tasmota.ErrTimeout):                                       false,
		fmt.Errorf("%w: Power1 may have been sent: %w", tasmota.ErrTimeout, tasmota.ErrUnreachable): false,
		errors.New("something else"):                                                                false,
	} {
		if got := NotPublished(err); got != want {
			t.Errorf("NotPublished(%v) = %v, want %v", err, got, want)
		}
	}
}
