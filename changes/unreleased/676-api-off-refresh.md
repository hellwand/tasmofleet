---
section: Fixed
---
A device whose HTTP API is off but that is online over MQTT now gets its last-known Settings values, policy values and GPIO template reading refreshed in the background; before, they stayed empty or old for as long as the API was off. The check that the answering device is the stored one now goes through MQTT for such a device and is asked once per refresh instead of up to three times; the rest of that refresh stays on MQTT and never falls back to HTTP, where another device could hold the address (#676)
