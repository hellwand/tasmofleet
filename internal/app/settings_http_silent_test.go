package app

import (
	"context"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hellwand/tasmofleet/internal/model"
	"github.com/hellwand/tasmofleet/internal/mqttbus"
	"github.com/hellwand/tasmofleet/internal/mqttcmd"
	"github.com/hellwand/tasmofleet/internal/settings"
	"github.com/hellwand/tasmofleet/internal/simulator"
	"github.com/hellwand/tasmofleet/internal/tasmota"
	"github.com/hellwand/tasmofleet/internal/transport"
)

// silentHTTP accepts connections at the device's HTTP address and never answers (a firewall that drops),
// counting the connections.
func silentHTTP(t *testing.T) (addr netip.AddrPort, conns *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	t.Cleanup(func() { close(done); _ = ln.Close() }) // test teardown
	conns = new(atomic.Int32)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			conns.Add(1)
			go func() { <-done; _ = c.Close() }() // held open, never answered; closed at teardown
		}
	}()
	return netip.MustParseAddrPort(ln.Addr().String()), conns
}

// Why (#702): the device of the report is online on our broker, but its HTTP does not answer. The Settings
// tab must read it over MQTT, including the identity check production wires (Prober.VerifyRead), without a
// single HTTP request: one request that times out discards the whole read. Simulated device, real broker,
// bus, command channel, router and transport facts.
func TestSettingsReadOfADeviceWhoseHTTPIsSilent(t *testing.T) {
	br, err := simulator.StartBroker()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = br.Close() }) // test teardown
	fleet, err := simulator.NewFleetWith(simulator.FleetOptions{FixturesDir: "../../testdata/fixtures", MQTTURL: br.URL(), TelePeriod: time.Hour}, simulator.Extra{TimeScale: 0.02})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fleet.Close() }) // test teardown
	dev, err := fleet.Add(t.Context(), simulator.DeviceSpec{Name: "silent", Profile: simulator.ProfileESP32, Version: "15.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	bus, err := mqttbus.Connect(t.Context(), mqttbus.Options{BrokerURL: br.URL(), ClientID: "silent-test"})
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(10 * time.Second); !bus.Connected(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("bus never connected")
		}
	}
	ctl, _ := simulator.Control(dev)
	readyCtx, readyCancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer readyCancel()
	if err := ctl.WaitFor(readyCtx, func(c simulator.Controller) bool { return c.MQTTReady() }); err != nil {
		t.Fatalf("simulated device never subscribed to its command topic: %v", err)
	}
	ch := mqttcmd.New(bus, mqttcmd.Options{Gap: 5 * time.Millisecond})
	t.Cleanup(ch.Close)
	addr, conns := silentHTTP(t)
	st := &gtStore{now: time.Now, devs: []model.Device{
		{ID: 1, Topic: dev.Topic(), Address: addr.Addr().String(), Port: int(addr.Port()), MQTT: model.MQTTOurs, MAC: ctl.MAC()},
	}}
	// a short HTTP timeout: without the fix the read fails fast instead of after the 10 s status timeout
	router := transport.NewRouter(transport.Options{HTTP: tasmota.NewHTTPClient(tasmota.Options{AllowedNetworks: loopbackNets(), Timeout: 300 * time.Millisecond}), Devices: st})
	router.SetFacts(&TransportFacts{Devices: st, Health: fakeFactsHealth{1: {LWT: "Online"}}, Memory: router.Memory(), Unanswered: router.UnansweredReads(),
		BusConnected: bus.Connected, FullTopic: func() string { return "%prefix%/%topic%/" }, FirmwareBaseURL: func() string { return "" },
		BrokerPassword: func() string { return "" }, Now: time.Now})
	router.SetMQTT(ch)
	pr := &Prober{Client: router.HTTPOnly(), Reads: router, Gap: time.Millisecond}
	set := settings.New(settings.Options{Devices: st, Client: router, ReadTimeout: 30 * time.Second, Verify: pr.VerifyRead})

	vals, err := set.Read(t.Context(), 1)
	if err != nil {
		t.Fatalf("Settings read: %v (HTTP connections: %d)", err, conns.Load())
	}
	if n := conns.Load(); n != 0 {
		t.Errorf("%d HTTP connection(s) to a device MQTT reaches", n)
	}
	// the Settings tab is admin-only and shows the login, as it does when the value comes over HTTP
	if vals["MqttUser"] == "" || vals["MqttUser"] == "***" {
		t.Errorf("MqttUser = %q, want the device's login", vals["MqttUser"])
	}
}
