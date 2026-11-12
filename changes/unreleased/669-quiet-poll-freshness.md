---
section: Fixed
---
Health: a device on your broker that sends no telemetry no longer flips to Stale and back between its polls when `POLL_INTERVAL` is longer than about 9 minutes (at 15 minutes that wrote about 190 history rows a day, at 2 hours the device showed Stale most of the day), and a start no longer writes two history rows for each such device about 11 minutes later. A poll's answer, or its refused HTTP API, now counts until the next poll is due; a device the poll no longer reaches is Stale from that poll on (#669)
