package connections

import (
	"net/url"
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
)

func TestBuildDSN(t *testing.T) {
	const password = "p@ss:w/o?r%d#99!"
	for _, engine := range []contract.Engine{contract.EnginePostgres, contract.EngineMySQL, contract.EngineMariaDB} {
		t.Run(string(engine), func(t *testing.T) {
			conn := &Connection{Engine: engine, Host: "localhost", DatabaseName: "app", Username: "user"}
			dsn, err := BuildDSN(conn, password)
			if err != nil {
				t.Fatal(err)
			}
			if engine == contract.EnginePostgres {
				u, err := url.Parse(dsn)
				if err != nil {
					t.Fatal(err)
				}
				got, _ := u.User.Password()
				if got != password || u.Host != "localhost:5432" || u.Path != "/app" || u.Query().Get("sslmode") != "disable" {
					t.Fatalf("incorrect PostgreSQL configuration: %v", u.Redacted())
				}
			} else {
				cfg, err := mysql.ParseDSN(dsn)
				if err != nil {
					t.Fatal(err)
				}
				if cfg.Passwd != password || cfg.Addr != "localhost:3306" || cfg.DBName != "app" {
					t.Fatal("incorrect MySQL/MariaDB configuration")
				}
			}
		})
	}
	if _, err := BuildDSN(&Connection{Engine: "unknown"}, password); err == nil {
		t.Fatal("unsupported engine accepted")
	}
}
