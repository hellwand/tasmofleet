---
section: Fixed
---
Discovery: a device whose `tasmota/discovery/<MAC>/config` message cannot be used (not JSON, no valid MAC or IP address, a MAC that differs from the topic) is now named in the log at the default level, once, with its MAC and the reason, never the message text; before, the debug line named neither. A broker reconnect that replays the same message does not write it again. MQTT messages that find the discovery queue full (discovery configs, LWT, STATE, INFO, Status 5 replies) are counted in `tasmofleet_discovery_proposals_dropped_total{kind="queue_full"}` (#715)
