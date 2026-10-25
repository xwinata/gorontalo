package database

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/url"
	"sync"

	"gorontalo/pkg/stderr"
)

// Config holds the settings required to connect to a database.
type Config struct {
	// Name is a logical identifier for this database.
	Name string

	// Dialect is the SQL dialect the database speaks.
	Dialect Dialect

	// Username is the account used to authenticate.
	Username string

	// Password is the credential for Username.
	Password string

	// Host is the server hostname or IP address.
	Host string

	// Port is the server port.
	Port int

	// Database is the name of the database/schema to connect to.
	Database string
}

// database is the default Database implementation. The underlying *sql.DB is
// opened lazily on the first Connection() call and reused afterwards.
type database struct {
	cfg Config

	// dsnFunc resolves the driver name and DSN. It defaults to cfg.dsn and is a
	// seam for tests to route Connection() to a registered fake driver.
	dsnFunc func() (driver, dsn string, err error)

	once sync.Once
	db   *sql.DB
	err  error
}

// New builds a Database from the given config. The actual connection is opened
// lazily on the first call to Connection().
func New(cfg Config) Database {
	d := &database{cfg: cfg}
	d.dsnFunc = d.cfg.dsn
	return d
}

func (d *database) Name() string {
	return d.cfg.Name
}

func (d *database) Dialect() Dialect {
	return d.cfg.Dialect
}

func (d *database) Connection() (*sql.DB, error) {
	d.once.Do(func() {
		dsnFunc := d.dsnFunc
		if dsnFunc == nil {
			dsnFunc = d.cfg.dsn
		}
		driver, dsn, err := dsnFunc()
		if err != nil {
			d.err = err
			return
		}
		d.db, d.err = sql.Open(driver, dsn)
	})
	return d.db, d.err
}

// dsn returns the driver name and data source name for the configured dialect.
func (c Config) dsn() (driver, dsn string, err error) {
	switch c.Dialect {
	case DialectPostgres:
		u := url.URL{
			Scheme: "postgres",
			User:   url.UserPassword(c.Username, c.Password),
			Host:   fmt.Sprintf("%s:%d", c.Host, c.Port),
			Path:   "/" + c.Database,
		}
		return "postgres", u.String(), nil
	case DialectMySQL:
		// user:pass@tcp(host:port)/dbname
		return "mysql", fmt.Sprintf(
			"%s:%s@tcp(%s:%d)/%s",
			c.Username, c.Password, c.Host, c.Port, c.Database,
		), nil
	case DialectMSSQL, DialectSQLServer:
		q := url.Values{}
		q.Set("database", c.Database)
		u := url.URL{
			Scheme:   "sqlserver",
			User:     url.UserPassword(c.Username, c.Password),
			Host:     fmt.Sprintf("%s:%d", c.Host, c.Port),
			RawQuery: q.Encode(),
		}
		return "sqlserver", u.String(), nil
	case DialectSQLite:
		// SQLite is file-based; Database holds the file path.
		return "sqlite", c.Database, nil
	case "":
		return "", "", stderr.New(nil, stderr.Options{
			Message: fmt.Sprintf("database %q: dialect is required", c.Name),
			Code:    "DIALECT_REQUIRED",
			Status:  http.StatusBadRequest,
		})
	default:
		return "", "", stderr.New(nil, stderr.Options{
			Message: fmt.Sprintf("database %q: unsupported dialect %q", c.Name, c.Dialect),
			Code:    "UNSUPPORTED_DIALECT",
			Status:  http.StatusBadRequest,
		})
	}
}
