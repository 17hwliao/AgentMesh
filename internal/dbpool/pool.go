// Package dbpool bounds the aggregate connection footprint of local services.
package dbpool

import (
	"database/sql"
	"time"
)

func Configure(db *sql.DB) {
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	db.SetConnMaxIdleTime(time.Minute)
	db.SetConnMaxLifetime(5 * time.Minute)
}
