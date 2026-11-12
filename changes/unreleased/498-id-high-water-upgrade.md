---
section: Upgrade notes
---
Migration 0047 adds the table `id_high_water`, seeded with the highest device and transaction id that the database or the audit log knows, so the ids of devices and transactions deleted before the upgrade are not taken again either. Nothing to do. Ids that were already reused before the upgrade cannot be repaired: such a device or transaction keeps showing the older audit rows of its predecessor (#498).
