# ClickHouse 使用指南

## 概述

本项目已经集成了 ClickHouse 支持，可以通过 GORM 操作 ClickHouse 数据库。

## 安装依赖

```bash
go get gorm.io/driver/clickhouse
```

## 基本用法

### 1. 初始化 ClickHouse 连接

```go
import "github.com/stones-hub/taurus-pro-storage/pkg/db"

err := db.InitDB(
    db.WithDBName("clickhouse_db"),
    db.WithDBType("clickhouse"),
    db.WithDSN("clickhouse://username:password@host:port/database?dial_timeout=10s&max_execution_time=60"),
    db.WithMaxOpenConns(10),
    db.WithMaxIdleConns(5),
)
```

### 2. 使用 DSN 构建函数

```go
dsn := db.DSN("clickhouse", "localhost", 9000, "default", "", "default", "")
// 输出: clickhouse://default:@localhost:9000/default?dial_timeout=10s&max_execution_time=60
```

### 3. 定义 ClickHouse 模型

```go
type User struct {
    ID        uint      `gorm:"primarykey"`
    Name      string    `gorm:"type:String"`           // ClickHouse String 类型
    Email     string    `gorm:"type:String"`
    Age       int       `gorm:"type:Int32"`           // ClickHouse Int32 类型
    CreatedAt time.Time `gorm:"type:DateTime"`        // ClickHouse DateTime 类型
    UpdatedAt time.Time `gorm:"type:DateTime"`
}
```

### 4. 基本 CRUD 操作

```go
// 创建
user := User{Name: "张三", Email: "zhangsan@example.com", Age: 25}
err := db.Create("clickhouse_db", &user)

// 查询
var users []User
err = db.Find("clickhouse_db", &users, "age > ?", 20)

// 更新
user.Age = 26
err = db.Update("clickhouse_db", &user)

// 删除
err = db.Delete("clickhouse_db", &user)
```

### 5. 原生 SQL 查询

```go
// 执行 SQL
var count int64
err = db.QuerySQL("clickhouse_db", &count, "SELECT count() FROM users WHERE age > ?", 20)

// 执行更新
err = db.ExecSQL("clickhouse_db", "UPDATE users SET age = ? WHERE id = ?", 30, 1)
```

### 6. 分页查询

```go
var users []User
err = db.Paginate("clickhouse_db", &users, 1, 10, "age > ?", 20)
```

## ClickHouse 特性支持

### 数据类型映射

| Go 类型 | ClickHouse 类型 | GORM 标签 |
|---------|----------------|-----------|
| string  | String         | `gorm:"type:String"` |
| int     | Int32          | `gorm:"type:Int32"` |
| int64   | Int64          | `gorm:"type:Int64"` |
| float64 | Float64        | `gorm:"type:Float64"` |
| bool    | UInt8          | `gorm:"type:UInt8"` |
| time.Time | DateTime     | `gorm:"type:DateTime"` |

### 注意事项

1. **主键**: ClickHouse 建议使用 `UInt32` 或 `UInt64` 类型作为主键
2. **索引**: ClickHouse 的索引机制与关系型数据库不同
3. **事务**: ClickHouse 对事务支持有限
4. **表引擎**: 建议使用 `MergeTree` 系列引擎以获得最佳性能

## 连接字符串参数

```
clickhouse://username:password@host:port/database?param1=value1&param2=value2
```

常用参数：
- `dial_timeout`: 连接超时时间
- `max_execution_time`: 查询最大执行时间
- `compress`: 是否启用压缩 (1/0)
- `secure`: 是否使用 SSL (1/0)

## 性能优化建议

1. **批量插入**: 使用批量插入提高写入性能
2. **分区**: 合理设计表分区策略
3. **索引**: 根据查询模式设计合适的索引
4. **压缩**: 选择合适的压缩算法
5. **连接池**: 合理配置连接池参数

## 示例代码

完整示例请参考 `examples/clickhouse_example/main.go`

## 故障排除

### 常见错误

1. **连接超时**: 检查网络连接和防火墙设置
2. **认证失败**: 验证用户名和密码
3. **类型不匹配**: 确保 Go 类型与 ClickHouse 类型兼容
4. **权限不足**: 检查数据库用户权限

### 调试技巧

1. 启用详细日志
2. 使用 ClickHouse 客户端测试连接
3. 检查 ClickHouse 服务器日志
4. 验证 DSN 格式是否正确
