package db

import "fmt"

// DSN build dsn by db type and params
func DSN(dbType, host string, port int, username, password, dbname string, sslmode string) string {
	if sslmode == "" {
		sslmode = "disable" // 默认禁用 SSL
	}

	switch dbType {
	case "mysql":
		// username:password@tcp(host:port)/dbname?charset=utf8mb4&parseTime=True&loc=Local
		return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=True&loc=Local",
			username, password, host, port, dbname)
	case "postgres":
		// host=localhost port=5432 user=postgres password=your_password dbname=your_db sslmode=disable
		return fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
			host, port, username, password, dbname, sslmode)
	case "sqlite":
		// 对于 SQLite，dbname 就是文件路径
		return dbname
	case "clickhouse":
		// clickhouse://username:password@host:port/database?dial_timeout=10s&max_execution_time=60
		return fmt.Sprintf("clickhouse://%s:%s@%s:%d/%s?dial_timeout=10s&max_execution_time=60",
			username, password, host, port, dbname)
	default:
		return ""
	}
}
