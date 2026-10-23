package connections

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/example/if3110-milestone1/internal/contract"
	"github.com/go-sql-driver/mysql"
)

// BuildDSN formats a database driver connection string for PostgreSQL, MySQL, or MariaDB.
func BuildDSN(conn *Connection, plainPassword string) (string, error) {
	switch conn.Engine {
	case contract.EnginePostgres:
		host := conn.Host
		if host == "" {
			host = "localhost"
		}
		port := conn.Port
		if port <= 0 {
			port = 5432
		}

		u := &url.URL{
			Scheme: "postgres",
			User:   url.UserPassword(conn.Username, plainPassword),
			Host:   net.JoinHostPort(host, strconv.Itoa(port)),
			Path:   conn.DatabaseName,
		}

		q := u.Query()
		sslMode := conn.SSLMode
		if sslMode == "" {
			sslMode = "disable"
		}
		q.Set("sslmode", sslMode)
		u.RawQuery = q.Encode()

		return u.String(), nil

	case contract.EngineMySQL, contract.EngineMariaDB:
		host := conn.Host
		if host == "" {
			host = "localhost"
		}
		port := conn.Port
		if port <= 0 {
			port = 3306
		}

		cfg := mysql.NewConfig()
		cfg.User = conn.Username
		cfg.Passwd = plainPassword
		cfg.Net = "tcp"
		cfg.Addr = net.JoinHostPort(host, strconv.Itoa(port))
		cfg.DBName = conn.DatabaseName
		cfg.ParseTime = true
		cfg.Loc = time.UTC

		switch strings.ToLower(conn.SSLMode) {
		case "disable", "":
			cfg.TLSConfig = "false"
		case "require":
			cfg.TLSConfig = "skip-verify"
		case "verify-ca", "verify-full":
			cfg.TLSConfig = "true"
		default:
			cfg.TLSConfig = conn.SSLMode
		}

		cfg.Params = map[string]string{
			"timeout":      "5s",
			"readTimeout":  "10s",
			"writeTimeout": "10s",
		}

		return cfg.FormatDSN(), nil

	default:
		return "", fmt.Errorf("unsupported engine: %s", conn.Engine)
	}
}
