# 数据库日志系统使用文档

## 概述

Taurus Pro Storage 提供了强大的数据库日志系统，支持多种日志格式和配置选项。该系统直接实现了 GORM 的 `logger.Interface` 接口，提供了灵活的日志记录功能。

## 特性

- **多种日志格式**：支持默认、JSON、简单和自定义格式
- **灵活的配置选项**：文件路径、日志级别、慢查询阈值等
- **自动日志轮转**：支持文件大小限制、备份数量、保存天数等
- **多数据库支持**：可以为不同的数据库连接配置不同的日志记录器
- **默认日志记录器**：当不指定日志记录器时，自动使用默认配置

## 日志级别

GORM 支持以下日志级别（从低到高）：

- **SILENT**：静默模式，不输出任何日志
- **ERROR**：只输出错误日志
- **WARN**：输出警告和错误日志
- **INFO**：输出所有日志（信息、警告、错误）

## 基本使用

### 1. 使用默认日志记录器

```go
import (
    "github.com/stones-hub/taurus-pro-storage/pkg/db"
    "gorm.io/gorm/logger"
)

// 初始化数据库，使用默认日志记录器
err := db.InitDB(
    db.WithDBName("mysql_db"),
    db.WithDBType("mysql"),
    db.WithDSN("root:password@tcp(127.0.0.1:3306)/test?charset=utf8mb4&parseTime=True&loc=Local"),
    db.WithLogger(nil), // 使用默认日志记录器
)
```

### 2. 使用自定义日志记录器

```go
// 创建自定义日志记录器
logger := db.NewDbLogger(
    db.WithLogFilePath("./logs/db/db.log"),
    db.WithLogLevel(logger.Info),
    db.WithLogFormatter(db.JSONLogFormatter),
)

// 初始化数据库，使用自定义日志记录器
err := db.InitDB(
    db.WithDBName("mysql_db"),
    db.WithDBType("mysql"),
    db.WithDSN("root:password@tcp(127.0.0.1:3306)/test?charset=utf8mb4&parseTime=True&loc=Local"),
    db.WithLogger(logger),
)
```

## 配置选项

### 日志文件配置

```go
logger := db.NewDbLogger(
    db.WithLogFilePath("./logs/db/db.log"), // 日志文件路径（空字符串表示输出到控制台）
)
```

### 日志级别配置

```go
logger := db.NewDbLogger(
    db.WithLogLevel(logger.Info), // 设置日志级别
)
```

### 慢查询阈值配置

```go
logger := db.NewDbLogger(
    db.WithSlowThreshold(500 * time.Millisecond), // 设置慢查询阈值
)
```

### 其他配置选项

```go
logger := db.NewDbLogger(
    db.WithIgnoreRecordNotFoundError(true), // 是否忽略记录未找到错误
    db.WithColorful(false),                 // 是否使用彩色输出
)
```

## 日志格式

### 1. 默认格式

```go
logger := db.NewDbLogger(
    db.WithLogFormatter(db.DefaultLogFormatter),
)
```

输出示例：
```
[INFO] [0.123ms] [rows:1] SELECT * FROM users WHERE id = 1
[WARN] [500.456ms] [rows:100] SELECT * FROM orders; slow sql >= 200ms
[ERROR] [1.234ms] [rows:0] SELECT * FROM non_existent_table; record not found
```

### 2. JSON格式

```go
logger := db.NewDbLogger(
    db.WithLogFormatter(db.JSONLogFormatter),
)
```

输出示例：
```json
{"level":"INFO","message":"[0.123ms] [rows:1] SELECT * FROM users WHERE id = 1","timestamp":"2025-01-15 10:30:45","service":"database"}
{"level":"WARN","message":"[500.456ms] [rows:100] SELECT * FROM orders; slow sql >= 200ms","timestamp":"2025-01-15 10:30:46","service":"database"}
```

### 3. 简单格式

```go
logger := db.NewDbLogger(
    db.WithLogFormatter(func(level logger.LogLevel, message string) string {
        return db.LogLevelToString(level) + ": " + message
    }),
)
```

输出示例：
```
INFO: [0.123ms] [rows:1] SELECT * FROM users WHERE id = 1
WARN: [500.456ms] [rows:100] SELECT * FROM orders; slow sql >= 200ms
```

### 4. 自定义格式

```go
customFormatter := func(level logger.LogLevel, message string) string {
    return fmt.Sprintf("[%s] %s | %s", 
        db.LogLevelToString(level), 
        time.Now().Format("2006-01-02 15:04:05"),
        message)
}

logger := db.NewDbLogger(
    db.WithLogFormatter(customFormatter),
)
```

输出示例：
```
[INFO] 2025-01-15 10:30:45 | [0.123ms] [rows:1] SELECT * FROM users WHERE id = 1
[WARN] 2025-01-15 10:30:46 | [500.456ms] [rows:100] SELECT * FROM orders; slow sql >= 200ms
```

## 多数据库配置示例

```go
// 数据库1：使用JSON格式日志
jsonLogger := db.NewDbLogger(
    db.WithLogFilePath("./logs/db/mysql.json.log"),
    db.WithLogLevel(logger.Info),
    db.WithLogFormatter(db.JSONLogFormatter),
)

err := db.InitDB(
    db.WithDBName("mysql_db"),
    db.WithDBType("mysql"),
    db.WithDSN("root:password@tcp(127.0.0.1:3306)/test"),
    db.WithLogger(jsonLogger),
)

// 数据库2：使用简单格式日志，只记录警告和错误
simpleLogger := db.NewDbLogger(
    db.WithLogFilePath("./logs/db/postgres.simple.log"),
    db.WithLogLevel(logger.Warn),
    db.WithLogFormatter(func(level logger.LogLevel, message string) string {
        return db.LogLevelToString(level) + ": " + message
    }),
)

err = db.InitDB(
    db.WithDBName("postgres_db"),
    db.WithDBType("postgres"),
    db.WithDSN("host=localhost user=postgres password=password dbname=test"),
    db.WithLogger(simpleLogger),
)
```

## 日志轮转配置

日志系统使用 `lumberjack` 进行日志轮转，支持以下配置：

- **MaxSize**：单个日志文件的最大大小（默认100MB）
- **MaxBackups**：保留的旧日志文件的最大数量（默认10个）
- **MaxAge**：日志文件的最大保存天数（默认30天）
- **Compress**：是否压缩旧日志文件（默认true）

```go
logger := db.NewDbLogger(
    db.WithLogFilePath("./logs/db/db.log"),
    // 日志轮转配置通过 lumberjack 自动处理
)
```

## 性能考虑

1. **日志级别选择**：在生产环境中，建议使用 `logger.Warn` 或 `logger.Error` 级别，避免过多的日志输出影响性能。

2. **文件输出 vs 控制台输出**：文件输出比控制台输出性能更好，特别是在高并发场景下。

3. **慢查询阈值**：合理设置慢查询阈值，避免记录过多的慢查询日志。

4. **日志轮转**：合理配置日志轮转参数，避免日志文件过大或过多。

## 最佳实践

1. **开发环境**：使用 `logger.Info` 级别和默认格式，便于调试。

2. **测试环境**：使用 `logger.Warn` 级别和JSON格式，便于问题排查。

3. **生产环境**：使用 `logger.Error` 级别和JSON格式，减少日志量并便于日志分析。

4. **日志文件管理**：定期清理旧日志文件，避免磁盘空间不足。

5. **监控集成**：将JSON格式的日志集成到日志分析系统中，便于监控和告警。

## 错误处理

```go
// 创建日志记录器时处理错误
logger, err := db.NewDbLogger(
    db.WithLogFilePath("./logs/db/db.log"),
)
if err != nil {
    log.Fatalf("创建日志记录器失败: %v", err)
}

// 初始化数据库时处理错误
err = db.InitDB(
    db.WithDBName("mysql_db"),
    db.WithDBType("mysql"),
    db.WithDSN("root:password@tcp(127.0.0.1:3306)/test"),
    db.WithLogger(logger),
)
if err != nil {
    log.Fatalf("数据库初始化失败: %v", err)
}
```

## 测试

运行测试用例：

```bash
go test ./pkg/db -v
```

测试覆盖了以下功能：
- 日志记录器创建和配置
- 不同日志格式的输出
- 日志级别过滤
- GORM接口实现
- 文件输出和控制台输出
- 自定义格式化函数 