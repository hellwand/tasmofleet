---
section: Fixed
---
Settings: the tab could not be read on a device that is online on TasmoFleet's broker but does not answer over HTTP ("The device did not answer in time."); the MQTT user was asked over HTTP first, and that timeout discarded the whole read. A bare `MqttUser` is now read like every other value, MQTT first; in the console it stays admin-only and its reply masked (#702, thanks @Noschvie)
