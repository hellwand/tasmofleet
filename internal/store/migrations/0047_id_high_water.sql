-- 0047_id_high_water: ids of devices and transactions are never handed out twice (#498).
--
-- devices.id and ota_batches.id are INTEGER PRIMARY KEY without AUTOINCREMENT, so SQLite gives a new row
-- max(id)+1: deleting the newest device (or PruneJobs removing the newest transaction) freed its id for the
-- next insert, and the new subject inherited everything that names the old one by value ("device:N" and
-- "transaction:N" in the audit log, links in notifications).
--
-- The tables are not rebuilt with AUTOINCREMENT: every migration runs in one transaction with foreign keys on,
-- where PRAGMA foreign_keys=OFF has no effect, so dropping the old devices table would cascade-delete
-- device_macs, health_events, ota_jobs and more, and set backups.device_id to NULL. Instead id_high_water keeps
-- the last id handed out per table; the store takes the next one from it inside the insert transaction
-- (MAX(last_id, MAX(id)) + 1) and inserts with that explicit id.
--
-- The seed is the highest id the table holds or the audit log names, so ids of devices and transactions
-- deleted before the upgrade are not taken again either ("batch:N" is the old spelling of "transaction:N").
-- Ids that were already reused before this migration cannot be told apart and stay as they are.
CREATE TABLE id_high_water (
    tbl     TEXT    PRIMARY KEY,
    last_id INTEGER NOT NULL
) WITHOUT ROWID;

INSERT INTO id_high_water (tbl, last_id) SELECT 'devices', MAX(
    (SELECT COALESCE(MAX(id), 0) FROM devices),
    (SELECT COALESCE(MAX(CAST(substr(target, 8) AS INTEGER)), 0) FROM audit_log WHERE target GLOB 'device:[0-9]*'));

INSERT INTO id_high_water (tbl, last_id) SELECT 'ota_batches', MAX(
    (SELECT COALESCE(MAX(id), 0) FROM ota_batches),
    (SELECT COALESCE(MAX(CAST(substr(target, 13) AS INTEGER)), 0) FROM audit_log WHERE target GLOB 'transaction:[0-9]*'),
    (SELECT COALESCE(MAX(CAST(substr(target, 7) AS INTEGER)), 0) FROM audit_log WHERE target GLOB 'batch:[0-9]*'));
