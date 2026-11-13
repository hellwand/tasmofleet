---
section: Fixed
---
CI: superseded Go caches no longer pile up on `main` and push the repository over the 10 GB cache limit. A job after `main.yml`, the nightly and the weekly run keeps the newest cache of each series (Go version and `go.sum` hash do not count) and deletes the rest (#692).
