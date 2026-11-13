---
section: Added
---
Update jobs can wait for a start time. At the start TasmoFleet checks every device again (the safety guard, the device lock, a firmware update still running, OTA exclusion). If TasmoFleet was down at the start time, the update starts as soon as it is back, but only within 60 minutes; later it is not started at all and a "Scheduled update not started" notification is sent (also with "only problems"). A planned update can be cancelled until it starts (#603)
