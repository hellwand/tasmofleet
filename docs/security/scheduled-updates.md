# Scheduled updates: who authorises a start, who can cancel (#604)

Code: `internal/ota/scheduled.go`, `internal/web/schedview.go`, `internal/web/updates.go`. Design: spec section 9.

A scheduled update starts as the system and is not authorised again at its start: it was authorised when it was created, exactly like an update that starts at once, and the audit entry `transaction.start` records who created it and the planned start (`start_at`). Any operator or admin can cancel it until it starts (Cancel in Transactions); a plan of a person who was disabled or demoted afterwards still starts. Until its start the plan keeps other TasmoFleet jobs, a restore and a device delete away from its devices, and at the start every check (device lock, firmware holds, `fwselect.Guard`) runs again.
