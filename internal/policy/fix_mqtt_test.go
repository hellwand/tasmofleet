package policy

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/hellwand/tasmofleet/internal/cmdpolicy"
	"github.com/hellwand/tasmofleet/internal/model"
	"github.com/hellwand/tasmofleet/internal/settings"
	"github.com/hellwand/tasmofleet/internal/tasmotacmd"
)

// Why (#606): Make compliant goes over MQTT first, where a command crosses the broker in clear text for every
// client allowed to read cmnd/#. The router keeps a write with a secret or a credential in any Backlog part on
// HTTP first (transport TestSecretWritesStayHTTPFirstWhileHTTPWorks), but Make compliant must never build one in
// the first place: a policy line that sets a password, a Wi-Fi name, a token, a key or a URL with a login, or that
// smuggles one in behind a "; ", is refused before a single command is sent. Every line of the table is one
// cmdpolicy would keep off MQTT, so the table cannot go stale into lines that prove nothing. Beyond the table, no
// line a policy can send (makeCompliantLines) is one cmdpolicy redacts or that carries a credential, and every
// secret setting of the form is refused.
func TestMakeCompliantNeverSendsASecret(t *testing.T) {
	for _, line := range []string{
		"MqttPassword x", "WebPassword x", "WebPassword2 x", "Password1 x", "SSId1 net", "TmToken x",
		"RgxPassword x", "RgxSSId net", "IfxPassword x", "LoRaWanAppKey 00", "WiFiTest1 net",
		"MI32Key 0123456789abcdef0123456789abcdef", "MI32Keys 0123456789abcdef", "ZbConfig 1",
		"OtaUrl http://u:p@h/fw.bin", "OtaUrl https://tok@h/fw.bin",
		"LedState 1; WebPassword x", "TelePeriod 60; OtaUrl http://u:p@h/fw.bin",
	} {
		v, err := cmdpolicy.Classify(line)
		secret := err != nil || v.Redact || cmdpolicy.CarriesCredential(line)
		if !secret {
			t.Fatalf("fixture: %q is no secret for cmdpolicy", line)
		}
		c := &fakeCmd{}
		err = newFixer(c, &fakeRead{vals: map[string]string{"SaveData": "0"}}).Apply(context.Background(), model.Device{ID: 1}, []string{"PowerOnState 0", line})
		if err == nil || len(c.sent) != 0 {
			t.Errorf("%q: err %v, sent %q", line, err, c.sent)
		}
	}
	for _, l := range makeCompliantLines(t) {
		if v, err := cmdpolicy.Classify(l.text); err != nil || v.Redact || cmdpolicy.CarriesCredential(l.text) {
			t.Errorf("a policy can send %q, which cmdpolicy keeps secret", l.text)
		}
	}
	for _, f := range settings.Fields {
		if !f.Secret {
			continue
		}
		if txt, err := CommandText(f.Key, "x"); err == nil {
			t.Errorf("secret setting %s: a policy can send %q", f.Key, txt)
		}
		if commandProblem(f.Key, "x") == nil {
			t.Errorf("secret setting %s: a policy rule may name it", f.Key)
		}
	}
}

// Why (#606): Tasmota reads the digits at the end of a command name as its index and runs the command without
// them, so "Topic1 plug" and "MqttPort1 plug" set the topic and the port like "Topic plug" and "MqttPort plug".
// Taken as a generic rule, such a spelling would skip the setting's validation (MqttPort1 plug would be sent) and
// its restart (settings.RestartsDevice knows Topic, not Topic1): Make compliant would put a line that restarts the
// device, or cuts it off from MQTT, among the first chunks and send the save after it, over MQTT, to a device that
// is restarting on another topic. A curated setting without an index of its own is therefore written only under
// its own name, in a rule as in a line.
func TestAnIndexedSpellingOfASettingIsThatSetting(t *testing.T) {
	checked := 0
	for _, f := range settings.Fields {
		if strings.TrimRight(f.Key, "0123456789") != f.Key {
			continue // FriendlyName1, NtpServer2: the index is the setting's own
		}
		for _, name := range indexed(f.Key) {
			if name == f.Key {
				continue
			}
			checked++
			for _, v := range sampleValues {
				if txt, err := CommandText(name, v); err == nil {
					t.Errorf("%s: a policy can send %q as a generic rule", f.Key, txt)
					break
				}
			}
			if _, err := ParseRules(rulesJSON(name, "1"), settings.Queryable); err == nil || !strings.Contains(err.Error(), "write the setting as "+f.Key) {
				t.Errorf("rule %s: %v, want a refusal naming %s", name, err, f.Key)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no indexed spelling was checked")
	}
}

// Why (#606): Tasmota runs a SetOption synonym as its SetOption (MqttTLS is SetOption103, MqttTLSFingerprint is
// SetOption132, kMqttSynonyms), with or without an index and in any letter case. Taken as a generic rule, such a name
// would skip what policy knows about the SetOption: that it restarts the device (settings.RestartsDevice) and cuts
// it off from MQTT (cmdpolicy names SetOption103 and 132, not MqttTLS). Make compliant would publish "MqttTLS 1" over
// MQTT first, among the first chunks with a save after it, to a device restarting into TLS, and over MQTT alone
// without the consent T10 demands. A synonym is therefore written only as its SetOption, in a rule as in a line;
// every synonym of every pinned catalogue, not a list picked by hand.
func TestASetOptionSynonymIsWrittenAsItsSetOption(t *testing.T) {
	set, err := tasmotacmd.Pinned()
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, v := range set.Versions() {
		cat, _, _ := set.For(v)
		for _, c := range cat.Commands {
			if !strings.HasPrefix(c.SynonymOf, "SetOption") {
				continue
			}
			want := "write the setting as " + c.SynonymOf
			for _, name := range []string{c.Command, strings.ToLower(c.Command), c.Command + "1"} {
				for _, val := range []string{"0", "1"} {
					checked++
					if txt, err := CommandText(name, val); err == nil || !strings.Contains(err.Error(), want) {
						t.Errorf("CommandText(%s, %s) = %q, %v; want a refusal naming %s", name, val, txt, err, c.SynonymOf)
					}
					if _, err := ParseRules(rulesJSON(name, val), settings.Queryable); err == nil || !strings.Contains(err.Error(), want) {
						t.Errorf("rule %s = %s: %v; want a refusal naming %s", name, val, err, c.SynonymOf)
					}
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("the pinned catalogues hold no SetOption synonym")
	}
}

// Why (#606): Tasmota reads a SetOption's value with strtol base 0 (support_command.ino:463), so a leading 0 means
// octal: "SetOption103 08" sets 0 and "SetOption103 018" sets 1 (both restart the device and SetOption103 cuts it
// off from MQTT). It reads the index as a plain decimal (atoi, :424), so "SetOption003" is SetOption3 (MQTT off).
// Read with Atoi, policy saw 8 and 18 (no restart, a value the device refuses) and missed the curated SetOption3
// with its 0/1 check, so Make compliant would send such a line among the first chunks, a save after it, and again
// on every run (Verify never reads "08" back). A zero-padded index or value is refused, in a line and in a rule: a
// stored rule whose line can never be built would be skipped on every run with a reason that blames its name.
// The plain spelling still goes.
func TestAZeroPaddedSetOptionIsRefused(t *testing.T) {
	for _, c := range [][2]string{
		{"SetOption103", "08"}, {"SetOption103", "018"}, {"SetOption132", "09"}, {"SetOption55", "08"},
		{"SetOption003", "08"}, {"SetOption003", "0"}, {"SetOption0103", "1"}, {"SetOption20", "01"},
	} {
		if txt, err := CommandText(c[0], c[1]); err == nil {
			t.Errorf("CommandText(%s, %s) = %q, want a refusal", c[0], c[1], txt)
		}
	}
	if _, err := ParseRules(rulesJSON("SetOption003", "0"), settings.Queryable); err == nil || !strings.Contains(err.Error(), "write the setting as SetOption3") {
		t.Errorf("rule SetOption003: %v, want a refusal naming SetOption3", err)
	}
	for _, c := range [][2]string{{"SetOption103", "08"}, {"SetOption20", "01"}, {"SetOption3", "00"}, {"SetOption55", "010"}, {"SetOption103", " 08"}} {
		if _, err := ParseRules(rulesJSON(c[0], c[1]), settings.Queryable); err == nil || !strings.Contains(err.Error(), "plain decimal") {
			t.Errorf("rule %s = %q: %v, want a refusal of the value", c[0], c[1], err)
		}
	}
	for _, c := range [][2]string{{"SetOption103", "0"}, {"SetOption20", "1"}, {"SetOption0", "0"}, {"SetOption55", "10"}} {
		if txt, err := CommandText(c[0], c[1]); err != nil || txt != c[0]+" "+c[1] {
			t.Errorf("CommandText(%s, %s) = %q, %v; the plain spelling must still go", c[0], c[1], txt, err)
		}
		if _, err := ParseRules(rulesJSON(c[0], c[1]), settings.Queryable); err != nil {
			t.Errorf("rule %s = %s: %v; the plain spelling must still be accepted", c[0], c[1], err)
		}
	}
}

// Why (#606): a chunk with a line that can cut the device off from MQTT goes over HTTP while HTTP works, and the
// chunks and the save after it would go over MQTT first to a device whose topic or broker may just have changed.
// That cannot happen because every such line Make compliant can send either restarts the device (chunks() puts it
// in the last chunks, and a run with a restart adds no save) or changes nothing on it: a stored rule may hold a
// SetOption that restarts at 0 and 1 with a value above 1, which Tasmota v16 answers with Command Error and does
// not apply (settings/fields.go, restartSetOptions), so the topic and the broker stay as they were. The lines are
// every one a policy can send (makeCompliantLines), not a list picked by hand: a cut-off command a policy could
// set without either breaks that and must be judged first. A line is judged as the device runs it (its catalogue
// name, a synonym as its SetOption, a leading 0 as octal), not only as policy reads it: "MqttTLS 1" is
// SetOption103 on the device, and policy, which ordered it as a plain line, must not accept it.
func TestEveryCutOffLineMakeCompliantCanSendRestartsOrIsRefusedByTheDevice(t *testing.T) {
	checked := map[string]bool{}
	for _, l := range makeCompliantLines(t) {
		runs := asTheDeviceRuns(t, l.key)
		if strings.HasPrefix(runs, "SetOption") && (runs != l.key || strconv.Itoa(atoi(l.value)) != l.value) {
			t.Errorf("%q: the device runs it as %s and reads a leading 0 in the value as octal; policy must read it the same way", l.text, runs)
		}
		if !cmdpolicy.CutsMQTTOff(l.text) && !cmdpolicy.CutsMQTTOff(runs+" "+l.value) {
			continue
		}
		checked[l.key] = true
		// the restart policy knows of (OrderCommands puts the line last by it), the refusal the device gives
		if !settings.RestartsDevice(l.key, l.value) && !refusedByTheDevice(runs, l.value) {
			t.Errorf("%q cuts MQTT off, does not restart the device for policy and is applied by it", l.text)
		}
	}
	// Not empty by accident: every setting of the form that cuts MQTT off and is no secret was among them.
	for _, f := range settings.Fields {
		if f.Secret {
			continue
		}
		cuts := false
		for _, v := range sampleValues {
			cuts = cuts || cmdpolicy.CutsMQTTOff(f.Key+" "+v)
		}
		if cuts && !checked[f.Key] {
			t.Errorf("%s cuts MQTT off but no line of it was checked: add a value it accepts to sampleValues", f.Key)
		}
	}
	if !checked["SetOption3"] || !checked["SetOption103"] || !checked["SetOption132"] {
		t.Errorf("the SetOptions that cut MQTT off were not all checked: %v", checked)
	}
}

// refusedByTheDevice reports whether key is a SetOption that restarts the device at 1 and value is a plain decimal
// above 1: Tasmota v16 answers that with Command Error and applies nothing (settings/fields.go, restartSetOptions).
// A value with a leading 0 is octal on the device ("08" is 0, "018" is 1) and is not refused there.
func refusedByTheDevice(key, value string) bool {
	v, err := strconv.Atoi(value)
	return strings.HasPrefix(key, "SetOption") && settings.RestartsDevice(key, "1") && err == nil && strconv.Itoa(v) == value && v > 1
}

// atoi is value as a number, -1 when it is none.
func atoi(value string) int {
	v, err := strconv.Atoi(value)
	if err != nil {
		return -1
	}
	return v
}

// asTheDeviceRuns is the command Tasmota v16 runs for name, read from its catalogue and not from policy's code: the
// table's spelling without the index, a SetOption synonym as its SetOption, and a SetOption's index as a number
// (SetOption003 is SetOption3). A name the catalogue does not know is returned as it is.
func asTheDeviceRuns(t *testing.T, name string) string {
	t.Helper()
	cat, err := tasmotacmd.Default()
	if err != nil {
		t.Fatal(err)
	}
	f, ok := cat.Find(name)
	if !ok {
		return name
	}
	for _, e := range f.Entries {
		if strings.HasPrefix(e.SynonymOf, "SetOption") {
			return e.SynonymOf
		}
	}
	if strings.EqualFold(f.Name, "SetOption") && f.HasIndex {
		return "SetOption" + strconv.Itoa(f.Index)
	}
	return f.Name
}

// sampleValues are values of every shape a setting of the form accepts: numbers for the switches, the ports and
// the SetOptions (also with a leading 0, which the device reads as octal), a topic, a full topic, a host, a login
// name and a URL.
var sampleValues = []string{"0", "1", "2", "3", "00", "08", "018", "255", "1883", "plug", "%prefix%/%topic%/", "broker.lan", "user", "http://h/fw.bin"}

// indexed is name as it is written alone and with each index Tasmota parses from it (0 to 9, and 01).
func indexed(name string) []string {
	out := []string{name}
	for _, i := range []string{"0", "1", "2", "3", "4", "5", "6", "7", "8", "9", "01"} {
		out = append(out, name+i)
	}
	return out
}

// makeCompliantLines is every line a policy can make Make compliant send, as parseLines accepts it: each name
// below, alone and with an index, with each of sampleValues, kept where CommandText gives exactly that line. The
// names come from the code, not from a list: every setting of the form and its lower-case spelling, every
// SetOption the form takes (SetOption0 to SetOption999, written with leading zeros too), the commands cmdpolicy
// names as cutting MQTT off (cutOffBases), and every command of every pinned Tasmota catalogue (a generic rule may
// name any of them, a SetOption synonym among them).
func makeCompliantLines(t *testing.T) []fixLine {
	t.Helper()
	set, err := tasmotacmd.Pinned()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, v := range set.Versions() {
		cat, _, _ := set.For(v)
		for _, c := range cat.Commands {
			names = append(names, indexed(c.Command)...)
		}
	}
	for _, f := range settings.Fields {
		names = append(names, indexed(f.Key)...)
		names = append(names, indexed(strings.ToLower(f.Key))...)
	}
	for _, b := range cutOffBases(t) {
		if b != "setoption" {
			names = append(names, indexed(b)...)
			if canon, ok := settings.Curated(b); ok {
				names = append(names, indexed(canon)...)
			}
		}
	}
	for n := range 1000 {
		for _, f := range []string{"%d", "%02d", "%03d"} {
			names = append(names, fmt.Sprintf("SetOption"+f, n))
		}
	}
	slices.Sort(names)
	names = slices.Compact(names)
	var out []fixLine
	for _, name := range names {
		for _, v := range sampleValues {
			if txt, err := CommandText(name, v); err == nil && txt == name+" "+v {
				out = append(out, fixLine{name, v, txt})
			}
		}
	}
	return out
}

// cutOffBases are the command names (lower case, without an index) that cmdpolicy's cutsMQTTOff names as cutting
// a device off from MQTT, read from its source: cmdpolicy keeps the list to itself, and a name added there must
// reach this guard without anyone copying it.
func cutOffBases(t *testing.T) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), filepath.Join("..", "cmdpolicy", "cmdpolicy.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var bases []string
	for _, d := range file.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Name.Name == "cutsMQTTOff" {
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if cc, ok := n.(*ast.CaseClause); ok {
					for _, e := range cc.List {
						if lit, ok := e.(*ast.BasicLit); ok && lit.Kind == token.STRING {
							s, _ := strconv.Unquote(lit.Value)
							bases = append(bases, s)
						}
					}
				}
				return true
			})
		}
	}
	if !slices.Contains(bases, "topic") || !slices.Contains(bases, "mqtthost") || !slices.Contains(bases, "setoption") {
		t.Fatalf("cutsMQTTOff in internal/cmdpolicy/cmdpolicy.go no longer reads as expected (found %q): update cutOffBases", bases)
	}
	return bases
}

// Why (#606): the synonyms come from the embedded catalogues; a catalogue that cannot be read must stop the
// server loudly, never leave policy with no synonyms, which would let MqttTLS pass as a generic rule again.
func TestADamagedCatalogueIsNeverReadAsNoSynonyms(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("synonymTable returned with a damaged catalogue")
		}
	}()
	synonymTable(nil, errors.New("damaged"))
}
