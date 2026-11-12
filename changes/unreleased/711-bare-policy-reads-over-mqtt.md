---
section: Fixed
---
Policy reads of `OtaUrl`, `PowerRetain`, `LogHost` and `SwitchMode1` to `SwitchMode28` go over MQTT first like every other read, so a device that is online on the broker but whose HTTP does not answer can be checked against these rules; who may send them in the console is unchanged (#711, reported in #702, thanks @Noschvie)
