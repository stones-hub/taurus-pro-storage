# Redis 日志功能

## 概述

Redis客户端支持通过Hook机制记录所有Redis操作的日志，包括连接、命令执行、管道操作等。这对于调试、监控和性能分析非常有用。

## 功能特性

- **连接日志**: 记录Redis连接建立和断开
- **命令日志**: 记录每个Redis命令的执行情况，包括参数、执行时间和结果
- **管道日志**: 记录Redis管道操作的执行情况
- **错误日志**: 详细记录错误信息和异常情况
- **性能监控**: 记录命令执行时间，便于性能分析
- **日志级别**: 支持DEBUG、INFO、WARN、ERROR四个日志级别
- **日志轮转**: 支持日志文件自动轮转和压缩
- **自定义格式化**: 支持自定义日志输出格式，包括JSON、简单格式等

## 使用方法

### 1. 创建日志记录器

```go
import "github.com/stones-hub/taurus-pro-storage/pkg/redisx"

// 创建文件日志记录器
logger, err := redisx.NewRedisLogger(
    redisx.WithLogFilePath("./logs/redis/redis.log"),
    redisx.WithLogLevel(redisx.LogLevelInfo),
    redisx.WithLogMaxSize(100),        // 100MB
    redisx.WithLogMaxBackups(10),      // 保留10个备份文件
    redisx.WithLogMaxAge(30),          // 保留30天
    redisx.WithLogCompress(true),      // 压缩旧日志文件
)
if err != nil {
    log.Fatalf("创建Redis日志记录器失败: %v", err)
}

// 创建控制台日志记录器
consoleLogger, err := redisx.NewRedisLogger(
    redisx.WithLogFilePath(""),        // 空路径表示输出到控制台
    redisx.WithLogLevel(redisx.LogLevelDebug),
)

// 创建JSON格式日志记录器
jsonLogger, err := redisx.NewRedisLogger(
    redisx.WithLogFilePath("./logs/redis/redis.json.log"),
    redisx.WithLogLevel(redisx.LogLevelInfo),
    redisx.WithLogFormatter(redisx.JSONLogFormatter),
)

// 创建简单格式日志记录器
simpleLogger, err := redisx.NewRedisLogger(
    redisx.WithLogFilePath("./logs/redis/redis.simple.log"),
    redisx.WithLogLevel(redisx.LogLevelInfo),
    redisx.WithLogFormatter(redisx.SimpleLogFormatter),
)

// 创建自定义格式日志记录器
customFormatter := func(level redisx.LogLevel, message string) string {
    return fmt.Sprintf("CUSTOM[%s] %s at %s", 
        level.String(), message, time.Now().Format("2006-01-02 15:04:05"))
}

customLogger, err := redisx.NewRedisLogger(
    redisx.WithLogFilePath("./logs/redis/redis.custom.log"),
    redisx.WithLogLevel(redisx.LogLevelInfo),
    redisx.WithLogFormatter(customFormatter),
)
```

### 2. 初始化Redis客户端（带日志）

```go
err := redisx.InitRedis(
    redisx.WithAddrs("localhost:6379"),
    redisx.WithPassword(""),
    redisx.WithDB(0),
    redisx.WithLogging(logger),        // 启用日志记录
)
if err != nil {
    log.Fatalf("Redis初始化失败: %v", err)
}
defer redisx.Redis.Close()
```

### 3. 执行Redis操作

```go
ctx := context.Background()

// 这些操作都会被记录到日志中
err := redisx.Redis.Set(ctx, "key", "value", time.Minute)
if err != nil {
    log.Printf("设置键值失败: %v", err)
}

value, err := redisx.Redis.Get(ctx, "key")
if err != nil {
    log.Printf("获取键值失败: %v", err)
}

// 哈希操作
err = redisx.Redis.HSet(ctx, "user:1", "name", "张三", "age", "25")
if err != nil {
    log.Printf("设置哈希失败: %v", err)
}
```

## 日志级别

### LogLevelDebug
记录所有详细信息，包括：
- 连接建立和断开
- 命令开始执行
- 命令执行完成
- 执行时间

### LogLevelInfo
记录重要操作信息，包括：
- 连接建立成功
- 命令执行成功
- 管道操作完成

### LogLevelWarn
记录警告信息，包括：
- 连接重试
- 命令执行超时

### LogLevelError
记录错误信息，包括：
- 连接失败
- 命令执行失败
- 异常情况

## 日志格式

### 连接日志
```
[INFO] Redis dialed tcp localhost:6379 in 2.5ms
[ERROR] Redis dial failed tcp localhost:6379 after 5s: connection refused
```

### 命令日志
```
[INFO] Redis command completed: [set key value ex 60] in 1.2ms
[INFO] Redis command completed: [get key] in 0.8ms
[DEBUG] Redis command completed (key not found): [get nonexistent] in 0.5ms
[ERROR] Redis command failed: [set key value] in 2.1ms, error: connection lost
```

### 管道日志
```
[INFO] Redis pipeline completed with 3 commands in 5.2ms
[ERROR] Redis pipeline failed with 2 commands in 3.1ms, error: timeout
```

## 配置选项

| 选项 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| FilePath | string | "./logs/redis/redis.log" | 日志文件路径，空字符串表示输出到控制台 |
| MaxSize | int | 100 | 单个日志文件的最大大小（MB） |
| MaxBackups | int | 10 | 保留的旧日志文件的最大数量 |
| MaxAge | int | 30 | 日志文件的最大保存天数 |
| Compress | bool | true | 是否压缩旧日志文件 |
| Level | LogLevel | LogLevelInfo | 日志级别 |
| Formatter | LogFormatter | DefaultLogFormatter | 日志格式化函数 |

## 内置格式化函数

### DefaultLogFormatter
默认格式：`[LEVEL] message`
```
[INFO] Redis command completed: [set key value ex 60] in 1.2ms
```

### JSONLogFormatter
JSON格式：包含级别、消息和时间戳
```json
{"level":"INFO","message":"Redis command completed: [set key value ex 60] in 1.2ms","timestamp":"2025-01-22T14:30:00Z"}
```

### SimpleLogFormatter
简单格式：`LEVEL: message`
```
INFO: Redis command completed: [set key value ex 60] in 1.2ms
```

## 性能考虑

- 日志记录会略微增加Redis操作的延迟
- 建议在生产环境中使用LogLevelInfo或LogLevelWarn级别
- 日志文件轮转可以防止磁盘空间耗尽
- 压缩旧日志文件可以节省存储空间

## 示例

完整的使用示例请参考 `bin/main.go` 文件中的Redis缓存示例部分。

## 测试

运行Redis日志功能测试：

```bash
go test ./pkg/redisx -v -run TestRedisLogger
go test ./pkg/redisx -v -run TestRedisWithLogging
```

## 注意事项

1. 确保日志目录有写入权限
2. 定期清理旧日志文件以节省磁盘空间
3. 在生产环境中谨慎使用DEBUG级别，避免日志文件过大
4. 日志文件路径支持相对路径和绝对路径
5. 如果Redis连接失败，日志记录器仍然可以正常工作 