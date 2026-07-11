// Package tune calculates hardware-specific configuration for the data
// layer. The InnoDB buffer pool is the single most important allocation on
// a WordPress server; sizing it wrong in either direction wastes the
// machine. Slipstream sizes it from measured RAM and the server's role.
package tune

import (
	"bytes"
	"fmt"
	"text/template"
)

// Role describes what else the machine runs besides MariaDB.
type Role string

const (
	// RoleShared: web server, PHP and database share this machine (the
	// default single-server install).
	RoleShared Role = "shared"
	// RoleDedicated: this machine only runs the database.
	RoleDedicated Role = "dedicated"
)

// MariaDBConfig is the calculated tuning for one machine.
type MariaDBConfig struct {
	BufferPoolMB     int64
	LogFileMB        int64
	MaxConnections   int
	TmpTableMB       int64
	TableOpenCache   int
	ThreadCacheSize  int
	IOCapacity       int
	FlushLogAtCommit int // 1 = full durability (commerce), 2 = performance
}

// CalculateMariaDB derives tuning from total system RAM and role.
//
// Shared machines keep most memory for PHP workers, the page cache and the
// OS: the buffer pool gets 25%. Dedicated database machines get 70%.
func CalculateMariaDB(memTotalMB int64, cpuCount int, role Role, commerce bool) MariaDBConfig {
	if memTotalMB <= 0 {
		memTotalMB = 2048
	}
	if cpuCount <= 0 {
		cpuCount = 2
	}

	var pool int64
	switch role {
	case RoleDedicated:
		pool = memTotalMB * 70 / 100
	default:
		pool = memTotalMB * 25 / 100
	}
	if pool < 128 {
		pool = 128
	}

	logFile := pool / 4
	if logFile < 64 {
		logFile = 64
	}
	if logFile > 2048 {
		logFile = 2048
	}

	conns := 100
	switch {
	case memTotalMB >= 16384:
		conns = 500
	case memTotalMB >= 8192:
		conns = 300
	case memTotalMB >= 4096:
		conns = 200
	}

	tmp := memTotalMB / 32
	if tmp < 32 {
		tmp = 32
	}
	if tmp > 256 {
		tmp = 256
	}

	flush := 2 // performance: at most 1s of transactions lost on power failure
	if commerce {
		flush = 1 // orders and payments require full durability
	}

	return MariaDBConfig{
		BufferPoolMB:     pool,
		LogFileMB:        logFile,
		MaxConnections:   conns,
		TmpTableMB:       tmp,
		TableOpenCache:   4000,
		ThreadCacheSize:  cpuCount * 8,
		IOCapacity:       1000, // SSD baseline; observed tuning can raise it
		FlushLogAtCommit: flush,
	}
}

// ConfigPath is where the rendered tuning file lives.
const ConfigPath = "/etc/mysql/mariadb.conf.d/99-slipstream.cnf"

// Render produces the MariaDB config file content.
func (c MariaDBConfig) Render() (string, error) {
	var buf bytes.Buffer
	if err := mariadbTmpl.Execute(&buf, c); err != nil {
		return "", err
	}
	return buf.String(), nil
}

var mariadbTmpl = template.Must(template.New("mariadb").Parse(
	`# Managed by Slipstream — calculated for this machine, do not edit.
# Changes are detected as drift. Re-tune from the panel instead.

[mysqld]
# InnoDB
innodb_buffer_pool_size = {{.BufferPoolMB}}M
innodb_log_file_size = {{.LogFileMB}}M
innodb_flush_log_at_trx_commit = {{.FlushLogAtCommit}}
innodb_flush_method = O_DIRECT
innodb_io_capacity = {{.IOCapacity}}
innodb_file_per_table = 1

# Connections and threads
max_connections = {{.MaxConnections}}
thread_cache_size = {{.ThreadCacheSize}}

# Temporary tables
tmp_table_size = {{.TmpTableMB}}M
max_heap_table_size = {{.TmpTableMB}}M

# Caches
table_open_cache = {{.TableOpenCache}}

# Observability for Velocity Engine recommendations
slow_query_log = 1
slow_query_log_file = /var/log/mysql/slow.log
long_query_time = 1
`))

func (c MariaDBConfig) String() string {
	return fmt.Sprintf("buffer_pool=%dM log=%dM conns=%d flush=%d",
		c.BufferPoolMB, c.LogFileMB, c.MaxConnections, c.FlushLogAtCommit)
}
