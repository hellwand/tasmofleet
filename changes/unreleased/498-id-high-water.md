---
section: Fixed
---
A deleted device or a transaction removed by the retention (`AUDIT_RETENTION`) no longer frees its id for the next new one: a new device took the id of the newest deleted device and showed its console history and audit rows, and old links and notifications opened it; a new transaction could show another one's audit rows the same way. Ids only grow now (#498)
