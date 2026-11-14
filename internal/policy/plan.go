package policy

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/hellwand/tasmofleet/internal/cmdpolicy"
	"github.com/hellwand/tasmofleet/internal/model"
	"github.com/hellwand/tasmofleet/internal/settings"
	"github.com/hellwand/tasmofleet/internal/tasmotacmd"
)

// "Make compliant" turns findings into the commands that remove the
// deviations. Only a deviation whose current value was read LIVE becomes a
// command: a value from an old backup says what the device had then, not what
// it has now, and an unknown value says nothing. Both are listed and skipped,
// never guessed. The ESP32 exception needs no code here: Check already leaves
// out the rules that do not apply to a device's family, tags or name.

// Why a deviation was not turned into a command.
const (
	SkipUnknown  = "the current value is unknown"
	SkipNotLive  = "only known from a backup, not read live"
	SkipConflict = "two rules expect different values"
)

// FixCommand is one command of a plan.
type FixCommand struct {
	Command string // the rule's setting, e.g. "SetOption0"
	Text    string // what is sent, e.g. "SetOption0 0"
	Have    string // the live value
	Want    string
}

// FixSkip is a deviation that is listed but not fixed.
type FixSkip struct {
	Command string
	Reason  string
}

// FixPlan is what "Make compliant" would do for one device. Commands are in
// the order they are sent (see OrderCommands).
type FixPlan struct {
	DeviceID model.DeviceID
	Commands []FixCommand
	Skipped  []FixSkip
}

// Lines are the command texts in sending order.
func (p FixPlan) Lines() []string {
	out := make([]string, len(p.Commands))
	for i, c := range p.Commands {
		out[i] = c.Text
	}
	return out
}

// A SetOption's index and value are plain decimals. Tasmota reads the index with atoi (SetOption003 is SetOption3,
// which deviceName names) and the value with strtol base 0, so a leading 0 in the value is octal there ("08" is 0)
// while Atoi reads 8 (#606).
var setOptionRe = regexp.MustCompile(`^SetOption(0|[1-9][0-9]{0,2})$`)

var setOptionValueRe = regexp.MustCompile(`^(0|[1-9][0-9]{0,4})$`)

// CommandText is the command that sets a rule's setting to want. The settings
// the editor knows are set with the editor's validation; a SetOption takes a
// plain number. Any other command (a generic rule) is set as "<Command> <want>"
// only when the command policy lets an admin send it without a confirmation:
// nothing that asks in the console is ever sent from a rule.
func CommandText(command, want string) (string, error) {
	want = canon(want)
	if name := deviceName(command); name != command {
		return "", fmt.Errorf("%s: write the setting as %s", command, name)
	}
	if f, ok := settings.Lookup(command); ok {
		if f.Secret {
			return "", fmt.Errorf("%s is write-only", command)
		}
		if err := f.Validate(want); err != nil {
			return "", err
		}
		return f.Command(want), nil
	}
	if setOptionRe.MatchString(command) && setOptionValueRe.MatchString(want) {
		if settings.SetOptionParam(command) {
			if err := settings.ValidateSetOptionParam(command, want); err != nil {
				return "", err
			}
		}
		return command + " " + want, nil
	}
	return genericText(command, want)
}

// deviceName is the name of the setting Tasmota runs for command when policy knows that setting, command itself
// otherwise. Tasmota matches a name in any letter case, reads the digits at its end as the index and runs the
// command without them (support_command.ino:417-424), reads a SetOption's index as a number (SetOption003 is
// SetOption3), and runs a SetOption synonym as its SetOption (MqttTLS is SetOption103). A rule or a line must use
// the name returned: under any other spelling it would pass as a generic rule, without the setting's validation
// and without what policy knows about it (settings.RestartsDevice, and cmdpolicy's cut-off check), so Make
// compliant would send a line that restarts the device, or cuts it off from MQTT, among the first chunks with a
// save after it (#606). FriendlyName1 and NtpServer2 are settings of their own; their names are left alone.
func deviceName(command string) string {
	const so = "SetOption"
	if len(command) > len(so) && strings.EqualFold(command[:len(so)], so) {
		if n, err := strconv.Atoi(command[len(so):]); err == nil && n >= 0 {
			return so + strconv.Itoa(n)
		}
	}
	if name, ok := settings.Curated(command); ok {
		return name
	}
	base := strings.TrimRight(command, "0123456789")
	if name, ok := settings.Curated(base); ok {
		return name
	}
	if name, ok := setOptionSynonyms()[strings.ToLower(base)]; ok {
		return name
	}
	return command
}

// setOptionSynonyms maps the lower-case name of every SetOption synonym of the pinned Tasmota catalogues to its
// SetOption.
var setOptionSynonyms = sync.OnceValue(func() map[string]string { return synonymTable(tasmotacmd.Pinned()) })

// synonymTable is setOptionSynonyms from the catalogues set. It panics on a damaged embedded catalogue, like
// tasmotacmd.GPIO (tasmotacmd's tests catch that): a catalogue that cannot be read must never read as "no synonyms".
func synonymTable(set *tasmotacmd.Set, err error) map[string]string {
	if err != nil {
		panic("policy: the embedded Tasmota catalogues: " + err.Error())
	}
	out := map[string]string{}
	for _, v := range set.Versions() {
		cat, _, _ := set.For(v)
		for _, c := range cat.Commands {
			if strings.HasPrefix(c.SynonymOf, "SetOption") {
				out[strings.ToLower(c.Command)] = c.SynonymOf
			}
		}
	}
	return out
}

// genericText is the set command of a generic rule.
func genericText(command, want string) (string, error) {
	if !wantRe.MatchString(want) {
		return "", fmt.Errorf("%s: the expected value is not a plain value", command)
	}
	if ok, why := cmdpolicy.PolicyReadable(command); !ok {
		return "", fmt.Errorf("%s cannot be set from the policy: %s", command, why)
	}
	text, err := cmdpolicy.PolicySetText(command, want)
	if err != nil {
		return "", fmt.Errorf("%s cannot be set from the policy: %w", command, err)
	}
	return text, nil
}

// PlanFix builds one plan per device that has at least one finding, in the
// order the devices first appear. live says whether the finding's value (or
// lack of one) comes from a live read of the device.
func PlanFix(findings []Finding, live func(model.DeviceID, string) bool) []FixPlan {
	var order []model.DeviceID
	plans := map[model.DeviceID]*FixPlan{}
	wants := map[model.DeviceID]map[string]string{}
	for _, f := range findings {
		p, ok := plans[f.DeviceID]
		if !ok {
			p = &FixPlan{DeviceID: f.DeviceID}
			plans[f.DeviceID] = p
			wants[f.DeviceID] = map[string]string{}
			order = append(order, f.DeviceID)
		}
		key := strings.ToLower(f.Rule.Command)
		skip := func(reason string) { p.Skipped = append(p.Skipped, FixSkip{f.Rule.Command, reason}) }
		switch {
		case f.Unknown:
			skip(SkipUnknown)
			continue
		case !live(f.DeviceID, key):
			skip(SkipNotLive)
			continue
		}
		if prev, dup := wants[f.DeviceID][key]; dup {
			if canon(prev) != canon(f.Rule.Want) {
				p.Commands = dropCommand(p.Commands, f.Rule.Command)
				skip(SkipConflict)
			}
			continue
		}
		text, err := CommandText(f.Rule.Command, f.Rule.Want)
		if err != nil {
			skip(err.Error())
			continue
		}
		wants[f.DeviceID][key] = f.Rule.Want
		p.Commands = append(p.Commands, FixCommand{Command: f.Rule.Command, Text: text, Have: f.Have, Want: f.Rule.Want})
	}
	out := make([]FixPlan, 0, len(order))
	for _, id := range order {
		p := plans[id]
		p.Commands = OrderCommands(p.Commands)
		out = append(out, *p)
	}
	return out
}

func dropCommand(cs []FixCommand, command string) []FixCommand {
	out := cs[:0:0]
	for _, c := range cs {
		if !strings.EqualFold(c.Command, command) {
			out = append(out, c)
		}
	}
	return out
}

// OrderCommands puts the commands in the order they must be sent. A command
// that restarts the device goes last (it would cut off whatever followed, and
// the restart saves everything), and SaveData goes right before those: its
// handler writes all settings to flash, so anything after it would stay in
// memory only. The rest keeps the rules' order.
func OrderCommands(cs []FixCommand) []FixCommand {
	var plain, save, restart []FixCommand
	for _, c := range cs {
		switch {
		case settings.RestartsDevice(c.Command, c.Want):
			restart = append(restart, c)
		case strings.EqualFold(c.Command, "SaveData"):
			save = append(save, c)
		default:
			plain = append(plain, c)
		}
	}
	return append(append(plain, save...), restart...)
}
