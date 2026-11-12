# Roadmap

The next releases and what they contain are the [GitHub milestones](https://github.com/hellwand/tasmofleet/milestones). This file holds only what no milestone does: the direction up to 1.0. No dates; details and bug reports live in the issues.

- **Security review** of the authentication, restore and command paths, before 1.0.
- **Firmware development channel**: track a pre-release or custom build per device group, next to the official releases.
- **Push upload for ESP8266.** Push upload (the update is sent to the device instead of the device downloading it) works on ESP32. On ESP8266 it is still refused, and those devices download; it follows after a test on real hardware.
- **Home Assistant app channels**: a stable and a beta channel in the app repository, so Home Assistant OS users can follow the same releases without a Docker host.
- **1.0** means a stable configuration and stable routes. From then on, breaking changes come only in major versions.
