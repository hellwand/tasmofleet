---
section: Fixed
---
A device with SetOption4 1 now answers Upgrade over MQTT at once: TasmoFleet reads its reply on `stat/<topic>/UPGRADE` (never the later download report there), so an update sent over MQTT keeps its firmware URL check instead of waiting 8 s for an unknown outcome. On such a device a Power command for a relay it does not have now fails with its Command Error instead of a timeout (#648)
