package cmdpolicy

import (
	"strings"
	"testing"
)

// TestScrubReply: device replies can carry credentials (Status 6 shows the
// MQTT user, Rule bodies and WebSend carry user:pass@host, URLs carry
// userinfo). Whatever is shown or stored from a reply goes through here.
func TestScrubReply(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"password key", `{"WebPassword":"hunter2"}`, `{"WebPassword":"***"}`},
		{"mqtt password", `{"MqttPassword":"hunter2","MqttHost":"broker"}`, `{"MqttPassword":"***","MqttHost":"broker"}`},
		{"mqtt user", `{"StatusMQT":{"MqttHost":"b","MqttUser":"bob","MqttPort":1883}}`, `{"StatusMQT":{"MqttHost":"b","MqttUser":"***","MqttPort":1883}}`},
		{"token and secret keys", `{"Token":"abc","client_secret":"x","ApiKey":"k"}`, `{"Token":"***","client_secret":"***","ApiKey":"***"}`},
		{"nested object under a secret key", `{"Password1":{"a":"b"}}`, `{"Password1":"***"}`},
		{"order is kept", `{"z":1,"a":2,"Password":"p","m":3}`, `{"z":1,"a":2,"Password":"***","m":3}`},
		{"array of objects", `{"x":[{"password":"p"},{"ok":"fine"}]}`, `{"x":[{"password":"***"},{"ok":"fine"}]}`},
		{"url userinfo", `{"Url":"http://bob:hunter2@example.org/x"}`, `{"Url":"http://***@example.org/x"}`},
		{"query secret", `{"Rule1":"ON x DO WebSend http://h/cm?user=a&password=hunter2 ENDON"}`, `{"Rule1":"ON x DO WebSend http://h/cm?user=a&password=*** ENDON"}`},
		{"websend bracket form", `{"Rule1":"WebSend [bob:hunter2@10.0.0.1:80] Power ON"}`, `{"Rule1":"WebSend [***@10.0.0.1:80] Power ON"}`},
		{"websend login after the host", `{"Rule1":"WebSend [h:80,bob:hunter2] Power ON"}`, `{"Rule1":"WebSend [h:80,***] Power ON"}`},
		{"token equals", `{"x":"auth token=abc123 done"}`, `{"x":"auth token=*** done"}`},
		{"userinfo without a scheme", `{"MqttHost":"alice:hunter2@broker"}`, `{"MqttHost":"***@broker"}`},
		{"ssid is not shown", `{"Wifi":{"SSId":"Home","RSSI":70}}`, `{"Wifi":{"SSId":"***","RSSI":70}}`},
		{"harmless stays", `{"StatusFWR":{"Version":"16.0.0","Hardware":"ESP8266EX"}}`, `{"StatusFWR":{"Version":"16.0.0","Hardware":"ESP8266EX"}}`},
		{"numbers stay exact", `{"Big":12345678901234567890,"F":1.50}`, `{"Big":12345678901234567890,"F":1.50}`},
		{"not json", `plain password=hunter2 text`, `plain password=*** text`},
		{"not json userinfo", `ftp://u:p@h/f`, `ftp://***@h/f`},
		{"escaped quote in value", `{"a":"say \"hi\" token=zzz"}`, `{"a":"say \"hi\" token=***"}`},
		// #605 review: ZbConfig answers with the Zigbee network key, MI32Key with the BLE bind keys. Matched by the exact
		// key: the part "key" alone would also mask harmless settings such as DzKeyIdx.
		{"zigbee network key", `{"ZbConfig":{"Channel":11,"KeyL":"0x0F0D0B0907050301L","KeyH":"0x0D0C0A0806040200L"}}`, `{"ZbConfig":{"Channel":11,"KeyL":"***","KeyH":"***"}}`},
		{"ble bind keys", `{"MIKeys":{"A4C138AABBCC":"00112233445566778899AABBCCDDEEFF"}}`, `{"MIKeys":"***"}`},
		{"a name with key in it stays", `{"DzKeyIdx1":5,"KeyLx":1}`, `{"DzKeyIdx1":5,"KeyLx":1}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := string(ScrubReply([]byte(tc.in)))
			if got != tc.want {
				t.Errorf("\n in:   %s\n got:  %s\n want: %s", tc.in, got, tc.want)
			}
			if strings.Contains(got, "hunter2") {
				t.Errorf("secret survived: %s", got)
			}
		})
	}
}

func TestScrubReplyKeepsEmptyAndGarbage(t *testing.T) {
	if got := ScrubReply(nil); len(got) != 0 {
		t.Errorf("nil -> %q", got)
	}
	if got := string(ScrubReply([]byte(`{"a":`))); got != `{"a":` {
		t.Errorf("truncated JSON is scrubbed as text, got %q", got)
	}
}

// Redacted also scrubs the arguments of commands we do not know to be secret:
// the audit log and history keep the text for a long time.
func TestRedactedScrubsArguments(t *testing.T) {
	tests := []struct{ cmd, want string }{
		{"FriendlyName1 x password=hunter2", "FriendlyName1 x password=***"},
		{"FriendlyName1 bob:hunter2@x", "FriendlyName1 ***@x"},
		{"Template {\"NAME\":\"x\"}", "Template {\"NAME\":\"x\"}"},
	}
	for _, tc := range tests {
		if got := Redacted(tc.cmd); got != tc.want {
			t.Errorf("Redacted(%q) = %q, want %q", tc.cmd, got, tc.want)
		}
	}
}

// Why (#605 review): the router keeps a write HTTP first while HTTP works when its text carries a credential,
// since over MQTT it would reach every client allowed to read cmnd/#. Such credentials sit in arguments of
// commands whose names are not secret: an OtaUrl with a login, Sendmail's SMTP login, WebSend's login. Any
// userinfo before a URL's host counts, a token alone or an empty user too: the device sends it as Basic auth. A
// command without one must not count, or every OtaUrl, WebSend and JSON argument would lose MQTT first; an '@' in
// the path is no userinfo.
func TestCarriesCredential(t *testing.T) {
	for _, c := range []string{
		"OtaUrl http://u:p@h/fw.bin", "OTAURL HTTP://U:P@H/F", "Backlog Power 1; OtaUrl http://u:p@h/f",
		"OtaUrl https://tok@h/fw.bin", "OtaUrl http://:p@h/fw.bin",
		"Sendmail [smtp.x:465:u:pw:a@b:c@d:s] body", "WebSend [h,u:pw] Power1 ON", "WebSend [u:pw@h] Power1 ON",
		"MqttHost u:pw@broker", "Rule1 ON x DO WebSend http://h/cm?user=a&password=pw ENDON",
	} {
		if !CarriesCredential(c) {
			t.Errorf("%q carries a credential", c)
		}
	}
	for _, c := range []string{
		"OtaUrl http://h/fw.bin", "OtaUrl http://h:80/x@y.bin", "WebSend [h:80] Power1 ON", "TelePeriod 60", "Power1 ON",
		`ZbSend {"Device":"0x1234","Write":[1,{"a":2}]}`, `Template {"NAME":"a:b","GPIO":[0,1]}`,
	} {
		if CarriesCredential(c) {
			t.Errorf("%q carries no credential", c)
		}
	}
}
