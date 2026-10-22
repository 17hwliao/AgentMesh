package tenant

import (
	"agentmesh/internal/dbpool"
	"database/sql"

	_ "github.com/go-sql-driver/mysql"
)

// OpenMySQLStore creates no network traffic itself; callers decide when to
// probe or query. The returned DB is owned by the caller.
func OpenMySQLStore(dsn string) (*MySQLStore, *sql.DB, error) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, nil, err
	}
	dbpool.Configure(db)
	return NewMySQLStore(stdReadDatabase{db: db}), db, nil
}
