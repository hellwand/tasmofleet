---
section: Changed
---
A second ruleset on `main`, "main: CI must be green (no bypass)", holds the status check `required` without an admin bypass, so nothing reaches `main` before `required` is green on that commit (#709)
