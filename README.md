# Taurus Pro Storage

[![Go Version](https://img.shields.io/badge/Go-1.27.3+-blue.svg)](https://golang.org)
[![License](https://img.shields.io/badge/License-Apache%202.0-green.svg)](LICENSE)
[![Go Report Card](https://goreportcard.com/badge/github.com/stones-hub/taurus-pro-storage)](https://goreportcard.com/report/github.com/stones-hub/taurus-pro-storage)

**Taurus Pro Storage** 是一个功能强大的 Go 语言存储解决方案库，提供了数据库连接管理、Redis 客户端、消息队列等核心功能。该库设计简洁、性能优异，支持多种数据库类型和队列引擎，是企业级应用的理想选择。

> **升级提示（破坏性变更，尚未发布）**：消息队列改为 Redis Stream 可靠消费（至少一次），删除了旧的
> Reader/Worker 等导出符号，键命名空间与旧版不兼容、旧数据不迁移，失败任务默认留存不再自动重试。
> 升级前请阅读 [CHANGELOG.md](CHANGELOG.md) 与 [docs/queue-reliability.md](docs/queue-reliability.md)；
> 验收范围与未验证项见 [docs/release-readiness.md](docs/release-readiness.md)。

## ✨ 主要特性

### 🗄️ 数据库管理
- **多数据库支持**: MySQL、PostgreSQL、SQLite
- **连接池管理**: 可配置的连接池大小、空闲连接数、连接生命周期
- **重试机制**: 自动重试失败的数据库连接
- **灵活配置**: 支持多种配置选项和自定义日志记录器
- **多连接管理**: 支持同时管理多个数据库连接
- **Repository模式**: 通用CRUD操作，大幅减少重复代码

### 🔴 Redis 客户端
- **高性能**: 基于 go-redis/v9，支持 Redis 4.0+
- **集群支持**: 支持单机、哨兵、集群模式
- **连接池**: 可配置的连接池和超时设置
- **版本检测**: 自动检测 Redis 版本并优化功能
- **结构化日志**: 支持 JSON 和简单格式的日志记录

### 📨 消息队列
- **多引擎支持**: Redis Stream 引擎（至少一次投递，进程崩溃后可恢复）和内存引擎（易失，进程退出即丢）
- **可靠投递**: 领取租约 + 自动续租、接管后旧凭证失效、延迟重试、失败默认留存并可查询/显式重放
- **稳定ID入队**: `AddDataWithID` 支持超时后安全重试与去重
- **并发处理**: 可配置的工作者数量，多进程/多实例共享同一队列
- **优雅关闭**: 停止领取、限时排空，超时如实返回错误；Manager 为单次生命周期
- **适用范围**: 可靠队列只支持单机/主从直连 Redis（拒绝 Cluster，拒绝 6.2 以下），实际只在 Redis 8.10.2 上验证；Processor 必须幂等

### 📝 日志系统
- **多格式支持**: JSON、简单文本格式
- **分级记录**: 可配置的日志级别
- **文件轮转**: 支持日志文件自动轮转
- **性能优化**: 异步写入，不影响主程序性能
- **自定义格式化器**: 支持自定义日志格式和Hook机制

## 🚀 快速开始

### 安装

```bash
go get github.com/stones-hub/taurus-pro-storage
```

### 基本使用

#### 1. 初始化 Redis

```go
package main

import (
    "github.com/stones-hub/taurus-pro-storage/pkg/redisx"
    "time"
)

func main() {
    // 创建Redis日志记录器
    redisLogger, _ := redisx.NewRedisLogger(
        redisx.WithLogFilePath("./logs/redis/redis.log"),
        redisx.WithLogLevel(redisx.LogLevelInfo),
        redisx.WithLogFormatter(redisx.JSONLogFormatter), // 使用JSON格式
    )

    // 初始化 Redis
    err := redisx.InitRedis(
        redisx.WithAddrs("127.0.0.1:6379"),
        redisx.WithPassword(""),
        redisx.WithDB(0),
        redisx.WithPoolSize(10),
        redisx.WithMinIdleConns(5),
        redisx.WithTimeout(5*time.Second, 3*time.Second, 3*time.Second),
        redisx.WithMaxRetries(3),
        redisx.WithLogging(redisLogger),
    )
    if err != nil {
        panic(err)
    }

    // 使用 Redis
    ctx := context.Background()
    redisx.Redis.Set(ctx, "key", "value", time.Minute)
    value, _ := redisx.Redis.Get(ctx, "key")
}
```

#### 2. 初始化数据库

```go
package main

import (
    "github.com/stones-hub/taurus-pro-storage/pkg/db"
    "time"
    "gorm.io/gorm/logger"
)

func main() {
    // 使用默认日志记录器
    err := db.InitDB(
        db.WithDBName("mysql_db"),
        db.WithDBType("mysql"),
        db.WithDSN("user:password@tcp(127.0.0.1:3306)/dbname?charset=utf8mb4&parseTime=True&loc=Local"),
        db.WithMaxOpenConns(10),
        db.WithMaxIdleConns(5),
        db.WithConnMaxLifetime(time.Hour),
        db.WithMaxRetries(3),
        db.WithRetryDelay(1),
        db.WithLogger(nil), // 使用默认日志记录器
    )
    if err != nil {
        panic(err)
    }

    // 获取数据库连接
    dbConn := db.GetDB("mysql_db")
    
    // 使用数据库连接
    var users []User
    dbConn.Find(&users)
}
```

#### 3. 使用 Repository 模式减少重复代码

Repository 模式是减少重复代码的核心特性，它提供了通用的CRUD操作接口：

```go
package main

import (
    "context"
    "github.com/stones-hub/taurus-pro-storage/pkg/db"
    "github.com/stones-hub/taurus-pro-storage/pkg/db/dao"
)

// User 实体
type User struct {
    ID       uint   `gorm:"primarykey"`
    Name     string `gorm:"size:255;not null"`
    Email    string `gorm:"size:255;uniqueIndex;not null"`
    Age      int    `gorm:"not null"`
    Status   string `gorm:"size:50;default:'active'"`
    CreateAt time.Time
}

// 实现 Entity 接口
func (u *User) TableName() string {
    return "users"
}

func (u *User) DB() *gorm.DB {
    return db.GetDB("mysql_db")
}

// UserRepository 用户数据访问层
type UserRepository struct {
    dao.Repository[User]
}

// NewUserRepository 创建用户Repository
func NewUserRepository() *UserRepository {
    repo, _ := dao.NewBaseRepositoryWithDB[User]()
    return &UserRepository{Repository: repo}
}

// 自定义业务方法
func (r *UserRepository) FindActiveUsers(ctx context.Context) ([]User, error) {
    return r.FindByCondition(ctx, "status = ?", "active")
}

func (r *UserRepository) FindUsersByAgeRange(ctx context.Context, minAge, maxAge int) ([]User, error) {
    return r.FindByCondition(ctx, "age BETWEEN ? AND ?", minAge, maxAge)
}

func (r *UserRepository) GetUserStats(ctx context.Context) (map[string]interface{}, error) {
    // 使用原生SQL查询统计信息
    sql := `
        SELECT 
            COUNT(*) as total_users,
            COUNT(CASE WHEN status = 'active' THEN 1 END) as active_users,
            AVG(age) as avg_age,
            MAX(created_at) as last_created
        FROM users
    `
    results, err := r.QueryToMap(ctx, sql)
    if err != nil {
        return nil, err
    }
    if len(results) > 0 {
        return results[0], nil
    }
    return nil, nil
}

func main() {
    // 创建Repository实例
    userRepo := NewUserRepository()
    ctx := context.Background()

    // 使用通用CRUD操作（无需重复编写）
    // 创建用户
    user := &User{Name: "张三", Email: "zhangsan@example.com", Age: 25}
    err := userRepo.Create(ctx, user)
    
    // 查找用户
    foundUser, err := userRepo.FindByID(ctx, 1)
    
    // 更新用户
    foundUser.Age = 26
    err = userRepo.Update(ctx, foundUser)
    
    // 分页查询
    users, total, err := userRepo.FindWithPagination(ctx, 1, 10, "created_at", true, "status = ?", "active")
    
    // 批量操作
    newUsers := []User{
        {Name: "李四", Email: "lisi@example.com", Age: 30},
        {Name: "王五", Email: "wangwu@example.com", Age: 28},
    }
    err = userRepo.CreateBatch(ctx, newUsers)
    
    // 事务操作
    err = userRepo.WithTransaction(ctx, func(tx *gorm.DB) error {
        // 创建用户
        user := &User{Name: "赵六", Email: "zhaoliu@example.com", Age: 32}
        if err := tx.Create(user).Error; err != nil {
            return err
        }
        
        // 创建用户配置（在同一事务中）
        // ... 其他操作
        return nil
    })
    
    // 使用自定义业务方法
    activeUsers, err := userRepo.FindActiveUsers(ctx)
    userStats, err := userRepo.GetUserStats(ctx)
}
```

**Repository 模式的优势：**

1. **消除重复代码**: 基础的CRUD操作只需实现一次，所有实体都可以复用
2. **类型安全**: 使用泛型确保类型安全，编译时就能发现类型错误
3. **统一接口**: 所有Repository都遵循相同的接口，便于维护和测试
4. **业务扩展**: 可以轻松添加自定义业务方法，同时保持基础功能的一致性
5. **事务支持**: 内置事务支持，简化复杂业务逻辑的实现

#### 4. 自定义日志格式化器和Hook

##### 数据库日志格式化器

```go
package main

import (
    "github.com/stones-hub/taurus-pro-storage/pkg/db"
    "gorm.io/gorm/logger"
)

func main() {
    // 自定义数据库日志格式化器
    customFormatter := func(level logger.LogLevel, message string) string {
        return fmt.Sprintf("[CUSTOM-DB] %s | %s | %s", 
            time.Now().Format("2006-01-02 15:04:05"),
            db.LogLevelToString(level),
            message)
    }

    // 创建自定义数据库日志记录器
    customDbLogger := db.NewDbLogger(
        db.WithLogFilePath("./logs/db/custom.log"),
        db.WithLogLevel(logger.Info),
        db.WithLogFormatter(customFormatter),
    )

    // 使用自定义日志记录器初始化数据库
    err := db.InitDB(
        db.WithDBName("custom_mysql_db"),
        db.WithDBType("mysql"),
        db.WithDSN("user:password@tcp(127.0.0.1:3306)/dbname"),
        db.WithLogger(customDbLogger), // 使用自定义日志记录器
    )
}
```

##### Redis日志格式化器和Hook

```go
package main

import (
    "github.com/stones-hub/taurus-pro-storage/pkg/redisx"
    "github.com/redis/go-redis/v9"
)

func main() {
    // 自定义Redis日志格式化器
    customRedisFormatter := func(level redisx.LogLevel, message string) string {
        return fmt.Sprintf(`{"custom":"true","level":"%s","message":"%s","timestamp":"%s","service":"redis"}`,
            level.String(), message, time.Now().Format(time.RFC3339))
    }

    // 创建自定义Redis日志记录器
    customRedisLogger, _ := redisx.NewRedisLogger(
        redisx.WithLogFilePath("./logs/redis/custom.log"),
        redisx.WithLogLevel(redisx.LogLevelDebug),
        redisx.WithLogFormatter(customRedisFormatter),
    )

    // 初始化Redis（自动添加日志Hook）
    err := redisx.InitRedis(
        redisx.WithAddrs("127.0.0.1:6379"),
        redisx.WithLogging(customRedisLogger), // 自动启用日志Hook
    )

    // 添加自定义Hook
    customHook := &CustomRedisHook{}
    redisx.Redis.AddHook(customHook)
}

// 自定义Redis Hook
type CustomRedisHook struct{}

// BeforeProcess 命令执行前的Hook
func (h *CustomRedisHook) BeforeProcess(ctx context.Context, cmd redis.Cmder) (context.Context, error) {
    // 记录命令执行开始
    log.Printf("即将执行Redis命令: %v", cmd.Args())
    
    // 可以修改上下文或返回错误来阻止命令执行
    return ctx, nil
}

// AfterProcess 命令执行后的Hook
func (h *CustomRedisHook) AfterProcess(ctx context.Context, cmd redis.Cmder) error {
    // 记录命令执行结果
    if cmd.Err() != nil {
        log.Printf("Redis命令执行失败: %v, 错误: %v", cmd.Args(), cmd.Err())
    } else {
        log.Printf("Redis命令执行成功: %v", cmd.Args())
    }
    return nil
}

// BeforeProcessPipeline 管道命令执行前的Hook
func (h *CustomRedisHook) BeforeProcessPipeline(ctx context.Context, cmds []redis.Cmder) (context.Context, error) {
    log.Printf("即将执行Redis管道命令，共%d个命令", len(cmds))
    return ctx, nil
}

// AfterProcessPipeline 管道命令执行后的Hook
func (h *CustomRedisHook) AfterProcessPipeline(ctx context.Context, cmds []redis.Cmder) error {
    log.Printf("Redis管道命令执行完成，共%d个命令", len(cmds))
    return nil
}

// DialHook 连接Hook
func (h *CustomRedisHook) DialHook(next redis.DialHook) redis.DialHook {
    return func(ctx context.Context, network, addr string) (net.Conn, error) {
        log.Printf("正在连接Redis: %s %s", network, addr)
        return next(ctx, network, addr)
    }
}

// ProcessHook 处理Hook
func (h *CustomRedisHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
    return func(ctx context.Context, cmd redis.Cmder) error {
        return next(ctx, cmd)
    }
}

// ProcessPipelineHook 管道处理Hook
func (h *CustomRedisHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
    return func(ctx context.Context, cmds []redis.Cmder) error {
        return next(ctx, cmds)
    }
}
```

##### 内置日志格式化器

```go
// 数据库日志格式化器
db.DefaultLogFormatter    // 默认格式: "2025-01-01 12:00:00 [INFO] message"
db.JSONLogFormatter       // JSON格式: {"level":"INFO","message":"message","timestamp":"2025-01-01 12:00:00","service":"database"}

// Redis日志格式化器
redisx.DefaultLogFormatter    // 默认格式: "2025-01-01 12:00:00 [INFO] message"
redisx.JSONLogFormatter       // JSON格式: {"level":"INFO","message":"message","timestamp":"2025-01-01 12:00:00","service":"redis"}
```

#### 5. 使用消息队列

```go
package main

import (
    "context"
    "errors"
    "time"

    "github.com/stones-hub/taurus-pro-storage/pkg/queue"
    "github.com/stones-hub/taurus-pro-storage/pkg/queue/common"
    "github.com/stones-hub/taurus-pro-storage/pkg/queue/engine"
    "github.com/stones-hub/taurus-pro-storage/pkg/redisx"
)

// Processor 必须幂等：至少一次投递意味着同一任务可能重复到达。
type MyProcessor struct{}

func (p *MyProcessor) Process(ctx context.Context, data []byte) error {
    // 返回 nil：成功；
    // 返回 common.NewRetryableError(err)：按退避重试，耗尽后失败留存；
    // panic：被捕获并按可重试错误处理；
    // 其它错误：直接失败留存（不会自动再次处理）。
    // ctx 在停止超时或租约丢失时被取消，长任务应响应取消。
    if len(data) == 0 {
        return errors.New("empty")
    }
    return common.NewRetryableError(errors.New("downstream busy"))
}

func main() {
    if err := redisx.InitRedis(redisx.WithAddrs("127.0.0.1:6379")); err != nil {
        panic(err)
    }

    manager, err := queue.NewManager(&MyProcessor{}, &queue.Config{
        EngineType:    engine.REDIS,
        Source:        "my_queue", // 必填；1-128 位字母数字及 . _ : -
        WorkerCount:   4,
        MaxRetries:    3, // 0 表示不重试
        RetryDelay:    5 * time.Second,
        LeaseDuration: 30 * time.Second,
    })
    if err != nil {
        panic(err)
    }
    if err := manager.Start(); err != nil {
        panic(err)
    }

    ctx := context.Background()
    // AddData 返回 nil 仅表示 Redis 已接收，不表示业务完成；超时/断连时结果不确定，
    // 重试会产生新任务。需要安全重试请使用稳定ID：
    _ = manager.AddData(ctx, []byte("payload"))
    _ = manager.AddDataWithID(ctx, "order-1001", []byte("payload"))

    // 失败任务默认留存，不会自动再处理：
    failed, _ := manager.ListFailed(ctx, 100) // 最早失败的至多 100 条（1..1000）
    for _, f := range failed {
        _ = manager.ReplayFailed(ctx, f.ID) // 同一ID转回就绪队列，重试预算重置
    }

    // 优雅关闭：停止领取，限时排空；超时返回 common.ErrStopTimeout。Stop 之后不能再次 Start。
    if err := manager.Stop(ctx); err != nil {
        panic(err)
    }
}
```

完整的外部调用示例见 `examples/reliable_queue_example`，运行语义、Redis 要求、迁移与回滚见 [docs/queue-reliability.md](docs/queue-reliability.md)。

## 📚 详细文档

### Repository 模式详解

Repository 模式是减少重复代码的核心，它提供了以下功能：

#### 基础 CRUD 操作
- **Create**: 创建单条记录
- **Save**: 保存记录（创建或更新）
- **Update**: 更新记录
- **Delete/DeleteByID**: 删除记录
- **FindByID**: 根据ID查找记录
- **FindAll**: 查找所有记录
- **FindByCondition**: 条件查询
- **FindOneByCondition**: 单条条件查询

#### 高级查询功能
- **FindWithPagination**: 通用分页查询
- **Count/CountByCondition**: 统计查询
- **Exists/ExistsByCondition**: 存在性检查
- **CreateBatch/UpdateBatch/DeleteBatch**: 批量操作

#### 事务和原生SQL支持
- **WithTransaction**: 事务操作
- **Exec**: 执行原生SQL
- **Query/QueryRaw**: 原生SQL查询
- **QueryWithPagination**: 原生SQL分页查询
- **QueryToStruct/QueryToMap**: 结果映射

#### 使用示例对比

**传统方式（重复代码）：**
```go
// 每个实体都要重复实现这些方法
func (s *UserService) CreateUser(user *User) error {
    return db.Create(user).Error
}

func (s *UserService) FindUserByID(id uint) (*User, error) {
    var user User
    err := db.First(&user, id).Error
    if err != nil {
        return nil, err
    }
    return &user, nil
}

func (s *UserService) UpdateUser(user *User) error {
    return db.Updates(user).Error
}

// ... 更多重复代码
```

**使用 Repository 模式：**
```go
// 只需实现一次，所有实体都可以复用
type UserRepository struct {
    dao.Repository[User]
}

// 自动获得所有基础CRUD方法
// 可以专注于添加业务特定的方法
func (r *UserRepository) FindActiveUsers(ctx context.Context) ([]User, error) {
    return r.FindByCondition(ctx, "status = ?", "active")
}
```

### 数据库配置选项

| 选项 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `DBName` | string | - | 数据库连接名称 |
| `DBType` | string | - | 数据库类型 (mysql, postgres, sqlite) |
| `DSN` | string | - | 数据库连接字符串 |
| `MaxOpenConns` | int | 25 | 最大打开连接数 |
| `MaxIdleConns` | int | 25 | 最大空闲连接数 |
| `ConnMaxLifetime` | time.Duration | 5分钟 | 连接最大生命周期 |
| `MaxRetries` | int | 3 | 连接尝试次数，**必须 > 0**，否则 `InitDB` 返回错误 |
| `RetryDelay` | int | 1 | 重试延迟（秒），不能为负 |

行为说明：
- 连接注册表并发安全；`DbList()` 返回快照，修改它不会影响内部状态；`GetDB` 找不到连接时错误可用 `errors.Is(err, db.ErrConnectionNotFound)` 判断。
- `FindByID` / `FindOneByCondition` 未找到记录时保留 `gorm.ErrRecordNotFound` 错误链，可用 `errors.Is(err, gorm.ErrRecordNotFound)`。
- `FindWithPagination` 的 `orderBy` 只接受实体模型中存在的字段名/列名（白名单），其它值返回错误；`page`、`pageSize` 必须 ≥ 1。`PaginateQuery` 的表名只接受标识符（可带 `schema.` 前缀）。
- 零值语义保持不变并有测试覆盖：`Save` / `db.Update` 写入全部字段（含零值）；`Repository.Update` 使用 GORM `Updates`，**忽略结构体中的零值字段**；需要写零值请用 `Save` 或 `UpdateByCondition` 传 map。
- 数据库封装语义仅用 SQLite 驱动验证，未覆盖 MySQL / Postgres / ClickHouse。

### Redis 配置选项

| 选项 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `Addrs` | []string | - | Redis 地址列表 |
| `Password` | string | "" | 密码 |
| `DB` | int | 0 | 数据库编号 |
| `PoolSize` | int | 10 | 连接池大小 |
| `MinIdleConns` | int | 5 | 最小空闲连接数 |
| `DialTimeout` | time.Duration | 5秒 | 连接超时时间 |
| `ReadTimeout` | time.Duration | 3秒 | 读取超时时间 |
| `WriteTimeout` | time.Duration | 3秒 | 写入超时时间 |
| `MaxRetries` | int | 3 | 最大重试次数 |

### 队列配置选项

| 选项 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `EngineType` | string | redis | `redis`（Stream，持久）或 `channel`（进程内，易失） |
| `Source` | string | - | **必填**，队列名称；同名队列的多个实例共享任务 |
| `RedisClient` | *redisx.RedisClient | `redisx.Redis` | 使用的 Redis 客户端 |
| `WorkerCount` | int | 5 | 工作协程数 |
| `MaxRetries` | int | 0（`DefaultConfig()` 为 3） | 最大重试次数；**0 表示不重试**，不会被默认值覆盖 |
| `RetryDelay` / `MaxRetryDelay` / `RetryFactor` | | 1s / 1m / 2 | 第 n 次重试延迟 = RetryDelay × RetryFactor^(n-1)，封顶 MaxRetryDelay |
| `LeaseDuration` | time.Duration | 30s | 领取租约；自动每 1/3 租期续租，崩溃后到期被恢复，须 ≥ 500ms |
| `DedupWindow` | time.Duration | 24h | `AddDataWithID` 任务完成后的去重保留时长（未完成任务不受限） |
| `FailedRetention` | time.Duration | 0 | 失败留存保留时长；0 表示永久保留直到重放 |
| `PollInterval` | time.Duration | 200ms | 无任务时的轮询间隔 |
| `Timeout` | time.Duration | 5m | `Stop` 排空在途任务的最长等待 |
| `QueueSize` | int | 1000 | 仅 `channel` 引擎：未清理任务（含失败留存）上限 |

`MergeWithDefaults`/`NewManager` 只为“0 没有意义”的字段补默认值，不会改写 `MaxRetries` 和任何 bool 字段；传入的 `Config` 不会被修改。

**已废弃字段**（设置后不会生效，`NewManager` 会在日志中提示）：`ReaderCount`、`Processing`、`Retry`、`Failed`、`EnableFailedQueue`、`EnableRetryQueue`、`FailedInterval`、`FailedBatch`、`RetryInterval`、`RetryBatch`、`EnableFailedDataToFile`、`FailedDataFilePath`。可靠路径不再有 Reader / Processing 队列，失败任务始终留存并可通过 `ListFailed`/`ReplayFailed` 处理，不再自动重新调用 Processor，也不再写 `/tmp` 文本文件。

### 日志系统配置

#### 数据库日志配置选项

| 选项 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `LogFilePath` | string | - | 日志文件路径（为空时输出到控制台） |
| `LogLevel` | logger.LogLevel | logger.Info | 日志级别 |
| `LogFormatter` | LogFormatter | DefaultLogFormatter | 日志格式化函数 |
| `MaxSize` | int | 100 | 单个日志文件最大大小（MB） |
| `MaxBackups` | int | 30 | 保留的旧日志文件数量 |
| `MaxAge` | int | 7 | 日志文件最大保存天数 |
| `Compress` | bool | false | 是否压缩旧日志文件 |

#### Redis日志配置选项

| 选项 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `FilePath` | string | - | 日志文件路径（为空时输出到控制台） |
| `Level` | LogLevel | LogLevelInfo | 日志级别 |
| `Formatter` | LogFormatter | DefaultLogFormatter | 日志格式化函数 |
| `MaxSize` | int | 100 | 单个日志文件最大大小（MB） |
| `MaxBackups` | int | 30 | 保留的旧日志文件数量 |
| `MaxAge` | int | 7 | 日志文件最大保存天数 |
| `Compress` | bool | false | 是否压缩旧日志文件 |

#### Hook机制说明

Redis Hook机制允许您在Redis操作的各个阶段插入自定义逻辑：

- **DialHook**: 连接建立前后的处理
- **ProcessHook**: 单个命令执行前后的处理
- **ProcessPipelineHook**: 管道命令执行前后的处理
- **自动日志Hook**: 启用日志记录器时自动添加的日志Hook

## 🔧 配置示例

### 环境变量配置

```bash
# Redis 配置
export REDIS_ADDRS=127.0.0.1:6379,127.0.0.1:6380
export REDIS_PASSWORD=your_password
export REDIS_DB=0
export REDIS_POOL_SIZE=20

# 数据库配置
export DB_TYPE=mysql
export DB_DSN=user:password@tcp(127.0.0.1:3306)/dbname
export DB_MAX_OPEN_CONNS=50
export DB_MAX_IDLE_CONNS=25
```

### 配置文件

```yaml
# config.yaml
redis:
  addrs:
    - "127.0.0.1:6379"
    - "127.0.0.1:6380"
  password: "your_password"
  db: 0
  pool_size: 20
  min_idle_conns: 10

database:
  mysql:
    dsn: "user:password@tcp(127.0.0.1:3306)/dbname"
    max_open_conns: 50
    max_idle_conns: 25
    conn_max_lifetime: "1h"

queue:
  engine_type: "redis"
  source: "my_queue"      # 必填
  worker_count: 4
  max_retries: 3          # 0 表示不重试
  retry_delay: "5s"
  lease_duration: "30s"
  dedup_window: "24h"
  failed_retention: "0s"  # 0 表示永久留存，需监控
```

（以上 YAML 只是示意，库本身不读取配置文件；字段对应 `queue.Config`。）

## 📊 性能特性

- **高并发**: 支持大量并发连接和操作
- **连接池**: 智能连接池管理，减少连接开销
- **异步处理**: 队列异步处理，提高吞吐量
- **内存优化**: 最小化内存分配和垃圾回收
- **超时控制**: 完善的超时机制，防止资源泄露
- **代码复用**: Repository模式大幅减少重复代码，提高开发效率

## 🧪 测试

运行测试套件：

```bash
# 运行所有测试（依赖 Redis 的测试在未设置 TAURUS_TEST_REDIS_ADDR 时会被跳过，不会连接 localhost:6379）
go test ./...

# 竞态检测
go test -race ./...

# 隔离的 Redis 集成与故障验收（固定镜像、随机端口、结束自动清理），详见 docs/queue-reliability.md
scripts/reliability-integration.sh

# 独立安装验收门禁：打包候选 → 本地 file module proxy → 只用公开 API 的独立调用模块；
# 私有 Redis 8.10.2 容器（1CPU/384MiB），逐ID账本对账、公开 API 契约、故障与负例。失败非零退出。
bash scripts/strict-readiness.sh --sustained
# 可选：附加 30 分钟有界长稳态（≤10 条/秒、≤18000 条、连续采样与安全阈值）
bash scripts/strict-readiness.sh --sustained --soak
# 参数负例自测
bash scripts/test-strict-readiness-args.sh

# 运行特定包的测试
go test ./pkg/db
go test ./pkg/redisx
go test ./pkg/queue

# 运行测试并显示覆盖率
go test -cover ./...

# 运行基准测试
go test -bench=. ./...
```

## 📁 项目结构

```
taurus-pro-storage/
├── bin/                    # 可执行文件
│   └── main.go           # 主程序示例
├── scripts/               # 隔离集成验收脚本
├── docs/                  # 文档
│   ├── db-logging.md     # 数据库日志文档
│   └── redis-logging.md  # Redis日志文档
├── examples/              # 示例代码
│   ├── queue_example/    # 队列配置/基础/高级示例
│   └── reliable_queue_example/  # 可靠队列外部调用示例
├── pkg/                   # 核心包
│   ├── db/               # 数据库管理
│   │   ├── dao/          # 数据访问对象
│   │   ├── db.go         # 数据库连接管理
│   │   ├── dsn.go        # 数据源名称处理
│   │   └── logger.go     # 数据库日志
│   ├── queue/            # 消息队列
│   │   ├── engine/       # 队列引擎
│   │   ├── config.go     # 队列配置
│   │   ├── manager.go    # 队列管理器
│   │   └── processor.go  # 消息处理器
│   └── redisx/           # Redis 扩展
│       ├── redis.go      # Redis 客户端
│       └── logger.go     # Redis 日志
├── logs/                  # 日志文件
├── go.mod                 # Go 模块文件
├── go.sum                 # Go 依赖校验
└── README.md             # 项目说明
```

## 🤝 贡献指南

我们欢迎所有形式的贡献！请查看以下指南：

1. Fork 本仓库
2. 创建特性分支 (`git checkout -b feature/AmazingFeature`)
3. 提交更改 (`git commit -m 'Add some AmazingFeature'`)
4. 推送到分支 (`git push origin feature/AmazingFeature`)
5. 开启 Pull Request

### 开发建议

- **使用 Repository 模式**: 新实体优先使用 Repository 模式，避免重复编写CRUD代码
- **保持接口一致**: 自定义Repository方法时，保持与基础接口的一致性
- **充分利用泛型**: 利用Go的泛型特性，确保类型安全和代码复用
- **编写测试**: 为Repository方法编写完整的测试用例

### 开发环境设置

```bash
# 克隆仓库
git clone https://github.com/stones-hub/taurus-pro-storage.git
cd taurus-pro-storage

# 安装依赖
go mod download

# 运行测试
go test ./...

# 构建项目
go build ./...
```

## 📄 许可证

本项目采用 Apache 2.0 许可证 - 查看 [LICENSE](LICENSE) 文件了解详情。

## 👥 作者

- **yelei** - *主要开发者* - [61647649@qq.com](mailto:61647649@qq.com)

## 🙏 致谢

感谢以下开源项目：

- [GORM](https://gorm.io/) - Go 语言的 ORM 库
- [go-redis](https://github.com/redis/go-redis) - Go 语言的 Redis 客户端
- [lumberjack](https://github.com/natefinch/lumberjack) - Go 语言的日志轮转库

## 📞 联系我们

- 项目主页: [https://github.com/stones-hub/taurus-pro-storage](https://github.com/stones-hub/taurus-pro-storage)
- 问题反馈: [Issues](https://github.com/stones-hub/taurus-pro-storage/issues)
- 邮箱: 61647649@qq.com

---

⭐ 如果这个项目对您有帮助，请给我们一个星标！
