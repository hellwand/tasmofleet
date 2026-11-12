package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Devices and transactions are named by id where nothing can follow a delete: "device:N" and
// "transaction:N" in the audit log, links in notifications. An id is therefore never handed out
// twice (#498): the next one comes from id_high_water (migration 0047), not from SQLite's
// max(id)+1, which frees the id of the newest row once it is deleted. MAX(id) is part of it so
// that rows written with an explicit id outside the store (the e2e seed) cannot cause a clash.
const (
	nextDeviceIDQuery = `UPDATE id_high_water SET last_id = MAX(last_id, (SELECT COALESCE(MAX(id), 0) FROM devices)) + 1
		WHERE tbl = 'devices' RETURNING last_id`
	nextBatchIDQuery = `UPDATE id_high_water SET last_id = MAX(last_id, (SELECT COALESCE(MAX(id), 0) FROM ota_batches)) + 1
		WHERE tbl = 'ota_batches' RETURNING last_id`
)

// nextID takes the next id with query (nextDeviceIDQuery or nextBatchIDQuery) inside tx, the
// transaction that inserts the row: a rollback gives the id back.
func nextID(ctx context.Context, tx *sql.Tx, query string) (int64, error) {
	var id int64
	err := tx.QueryRowContext(ctx, query).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, errors.New("store: id_high_water has no row for this table (migration 0047)")
	}
	if err != nil {
		return 0, fmt.Errorf("store: next id: %w", err)
	}
	return id, nil
}
