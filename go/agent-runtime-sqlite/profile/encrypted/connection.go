// Package encrypted opens private SQLite profiles through the Adiantum VFS.
package encrypted

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"net/url"
	"runtime"
	"strings"
	"time"

	"github.com/ncruces/go-sqlite3"
	sqlitedriver "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/vfs/adiantum"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite/profile"
)

// WithKey uses the same key for staging, final reopen and pool reconnects.
// The caller owns the key and must keep it until all returned pools have closed.
func WithKey(key *[32]byte) profile.OpenConnection {
	return func(uri url.URL) (*sql.DB, error) {
		if key == nil {
			return nil, errors.New("encrypted SQLite profile requires a key")
		}
		params := uri.Query()
		pragmas := params["_pragma"]
		// Defer pragma execution until after key installation. The URI contains
		// only the driver settings, never the raw key or an encoded equivalent.
		params.Del("_pragma")
		params.Del("_timezone")
		params.Del("_time_format")
		params.Set("vfs", privateVFSName)
		params.Set("_timefmt", "2006-01-02 15:04:05.000000000")
		uri.RawQuery = params.Encode()
		if runtime.GOOS == "windows" && len(uri.Path) > 3 && uri.Path[0] == '/' && uri.Path[2] == ':' {
			// The Go VFS expects a drive path, including for escaped filenames.
			uri.Opaque = strings.TrimPrefix(uri.EscapedPath(), "/")
			uri.Path, uri.RawPath = "", ""
		}
		base, err := (&sqlitedriver.SQLite{}).OpenConnector(uri.String())
		if err != nil {
			return nil, err
		}
		return sql.OpenDB(&connector{Connector: base, key: key, pragmas: pragmas}), nil
	}
}

type connector struct {
	driver.Connector
	key     *[32]byte
	pragmas []string
}

type nativeConnection interface {
	sqlitedriver.Conn
	driver.ExecerContext
	driver.Validator
	driver.NamedValueChecker
}

func (c *connector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	native := conn.(nativeConnection)
	if err = c.configure(native.Raw()); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return utcConnection{native}, nil
}

func (c *connector) configure(db *sqlite3.Conn) error {
	// Require the VFS acknowledgement. Plain SQLite ignores unknown PRAGMAs.
	stmt, _, err := db.Prepare("PRAGMA hexkey='" + hex.EncodeToString(c.key[:]) + "'")
	if err != nil {
		return errors.New("SQLite encryption key rejected")
	}
	ok := stmt.Step() && stmt.ColumnText(0) == "ok"
	if err := stmt.Close(); err != nil || !ok {
		return errors.New("SQLite encryption VFS did not accept the key")
	}
	for _, pragma := range c.pragmas {
		if err := db.Exec("PRAGMA " + pragma); err != nil {
			return err
		}
	}
	return nil
}

// Preserve instant ordering (including sub-millisecond differences) when the
// driver binds a time in an arbitrary location to a SQLite DATETIME column.
type utcConnection struct{ nativeConnection }

func (c utcConnection) CheckNamedValue(v *driver.NamedValue) error {
	utcValue(v)
	return c.nativeConnection.CheckNamedValue(v)
}

func (c utcConnection) Prepare(query string) (driver.Stmt, error) {
	return c.PrepareContext(context.Background(), query)
}

func (c utcConnection) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	stmt, err := c.nativeConnection.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	return utcStatement{stmt.(nativeStatement)}, nil
}

type nativeStatement interface {
	driver.Stmt
	driver.StmtExecContext
	driver.StmtQueryContext
	driver.NamedValueChecker
}

type utcStatement struct{ nativeStatement }

func (s utcStatement) CheckNamedValue(v *driver.NamedValue) error {
	err := s.nativeStatement.CheckNamedValue(v)
	if errors.Is(err, driver.ErrSkip) {
		// Apply the same conversions as database/sql before normalizing values
		// returned by pointers and driver.Valuers such as sql.NullTime.
		v.Value, err = driver.DefaultParameterConverter.ConvertValue(v.Value)
	}
	if err != nil {
		return err
	}
	utcValue(v)
	return nil
}

func utcValue(v *driver.NamedValue) {
	if t, ok := v.Value.(time.Time); ok {
		v.Value = t.UTC()
	}
}
