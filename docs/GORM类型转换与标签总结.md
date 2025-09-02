# GORM类型转换与标签总结

## 目录
1. [Scan和Value接口](#scan和value接口)
2. [type标签的作用](#type标签的作用)
3. [默认类型转换规则](#默认类型转换规则)
4. [实际应用示例](#实际应用示例)
5. [常见问题和解决方案](#常见问题和解决方案)
6. [最佳实践](#最佳实践)

---

## Scan和Value接口

### 1. 接口定义

```go
import "database/sql/driver"

// Scanner 接口：从数据库读取数据时使用
type Scanner interface {
    Scan(value interface{}) error
}

// Valuer 接口：向数据库写入数据时使用
type Valuer interface {
    Value() (driver.Value, error)
}
```

### 2. 接口作用

#### 2.1 **Scan接口**
- **作用**：从数据库读取数据并转换为Go类型
- **调用时机**：执行查询操作时（Find、First、FindAll等）
- **参数**：数据库返回的原始值（可能是string、[]byte、int64等）

#### 2.2 **Value接口**
- **作用**：将Go类型转换为数据库可以存储的格式
- **调用时机**：执行写入操作时（Create、Save、Update等）
- **返回值**：driver.Value类型（string、[]byte、int64、float64、bool、time.Time、nil）

### 3. 实现示例

```go
// 自定义状态类型
type Status string

const (
    StatusPending   Status = "pending"
    StatusCompleted Status = "completed"
    StatusCancelled Status = "cancelled"
)

// 实现Scanner接口（从数据库读取时）
func (s *Status) Scan(value interface{}) error {
    if value == nil {
        *s = ""
        return nil
    }
    
    switch v := value.(type) {
    case string:
        *s = Status(v)
    case []byte:
        *s = Status(v)
    case int64:
        // 如果是数字，转换为状态
        switch v {
        case 0:
            *s = StatusPending
        case 1:
            *s = StatusCompleted
        case 2:
            *s = StatusCancelled
        default:
            return fmt.Errorf("invalid status value: %d", v)
        }
    default:
        return fmt.Errorf("cannot scan %T into Status", value)
    }
    return nil
}

// 实现Valuer接口（写入数据库时）
func (s Status) Value() (driver.Value, error) {
    if s == "" {
        return nil, nil
    }
    return string(s), nil
}
```

### 4. 工作流程

#### 4.1 **写入数据库流程**
```go
// 1. 创建数据
order := Order{
    ID:     1,
    Status: StatusPending,  // Go类型
}

// 2. 保存到数据库
db.Create(&order)

// 3. GORM内部调用Value()方法
// order.Status.Value() -> 返回 "pending" (string)

// 4. 执行SQL
// INSERT INTO orders (id, status) VALUES (1, 'pending')
```

#### 4.2 **从数据库读取流程**
```go
// 1. 查询数据
var order Order
db.First(&order, 1)

// 2. 数据库返回原始数据
// 数据库返回: status = "pending" (string)

// 3. GORM内部调用Scan()方法
// order.Status.Scan("pending") -> 转换为Status("pending")

// 4. 结果
// order.Status = StatusPending
```

---

## type标签的作用

### 1. 主要用途

`type` 标签用于**定义数据库表结构**，告诉GORM在创建表时应该使用什么数据库类型。

```go
type User struct {
    ID       uint   `gorm:"primaryKey"`
    Name     string `gorm:"type:varchar(100)"`        // 指定数据库类型
    Email    string `gorm:"type:varchar(255);unique"` // 指定类型+约束
    Age      int    `gorm:"type:int"`                 // 指定整数类型
    Salary   float64 `gorm:"type:decimal(10,2)"`      // 指定小数类型
    Bio      string `gorm:"type:text"`                // 指定文本类型
    Avatar   []byte `gorm:"type:blob"`                // 指定二进制类型
}
```

### 2. AutoMigrate时使用

```go
// 当执行AutoMigrate时，GORM会根据type标签创建表结构
db.AutoMigrate(&User{})

// 生成的SQL（MySQL）：
// CREATE TABLE users (
//     id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
//     name VARCHAR(100),
//     email VARCHAR(255) UNIQUE,
//     age INT,
//     salary DECIMAL(10,2),
//     bio TEXT,
//     avatar BLOB
// );
```

### 3. 不同数据库的type标签

#### 3.1 **MySQL**
```go
type User struct {
    ID       uint   `gorm:"primaryKey"`
    Name     string `gorm:"type:varchar(100)"`
    Email    string `gorm:"type:varchar(255);unique"`
    Age      int    `gorm:"type:int"`
    Salary   float64 `gorm:"type:decimal(10,2)"`
    Bio      string `gorm:"type:text"`
    Avatar   []byte `gorm:"type:blob"`
    Status   string `gorm:"type:enum('active','inactive')"`
    CreatedAt time.Time `gorm:"type:datetime"`
}
```

#### 3.2 **PostgreSQL**
```go
type User struct {
    ID       uint   `gorm:"primaryKey"`
    Name     string `gorm:"type:varchar(100)"`
    Email    string `gorm:"type:varchar(255);unique"`
    Age      int    `gorm:"type:integer"`
    Salary   float64 `gorm:"type:numeric(10,2)"`
    Bio      string `gorm:"type:text"`
    Avatar   []byte `gorm:"type:bytea"`
    Status   string `gorm:"type:varchar(20);check:status IN ('active','inactive')"`
    CreatedAt time.Time `gorm:"type:timestamp"`
}
```

#### 3.3 **SQLite**
```go
type User struct {
    ID       uint   `gorm:"primaryKey"`
    Name     string `gorm:"type:text"`
    Email    string `gorm:"type:text;unique"`
    Age      int    `gorm:"type:integer"`
    Salary   float64 `gorm:"type:real"`
    Bio      string `gorm:"type:text"`
    Avatar   []byte `gorm:"type:blob"`
    Status   string `gorm:"type:text"`
    CreatedAt time.Time `gorm:"type:datetime"`
}
```

---

## 默认类型转换规则

### 1. Insert操作（Value转换）

如果不实现`Value`接口，GORM会根据Go类型进行默认转换：

```go
// Go类型 -> 数据库类型映射
int, int8, int16, int32, int64     -> INT, BIGINT
uint, uint8, uint16, uint32, uint64 -> INT, BIGINT (无符号)
float32, float64                    -> FLOAT, DOUBLE
string                              -> VARCHAR, TEXT
bool                                -> BOOLEAN, TINYINT
time.Time                           -> DATETIME, TIMESTAMP
[]byte                              -> BLOB, VARBINARY
```

### 2. Select操作（Scan转换）

如果不实现`Scan`接口，GORM会根据Go结构体字段类型进行默认转换：

```go
// 数据库类型 -> Go类型映射
INT, BIGINT           -> int64
FLOAT, DOUBLE         -> float64
VARCHAR, TEXT         -> string
BOOLEAN, TINYINT      -> bool
DATETIME, TIMESTAMP   -> time.Time
BLOB, VARBINARY       -> []byte
```

### 3. 默认转换示例

```go
type User struct {
    ID        int       `gorm:"primaryKey"`
    Name      string    `gorm:"column:name"`
    Age       int       `gorm:"column:age"`
    Salary    float64   `gorm:"column:salary"`
    IsActive  bool      `gorm:"column:is_active"`
    CreatedAt time.Time `gorm:"column:created_at"`
    Avatar    []byte    `gorm:"column:avatar"`
}

// 插入数据时
user := User{
    ID:        1,
    Name:      "张三",
    Age:       25,
    Salary:    5000.50,
    IsActive:  true,
    CreatedAt: time.Now(),
    Avatar:    []byte("image_data"),
}

db.Create(&user)

// GORM内部转换：
// ID: 1 (int) -> 1 (INT)
// Name: "张三" (string) -> '张三' (VARCHAR)
// Age: 25 (int) -> 25 (INT)
// Salary: 5000.50 (float64) -> 5000.50 (DOUBLE)
// IsActive: true (bool) -> 1 (TINYINT)
// CreatedAt: time.Time -> '2024-01-01 12:00:00' (DATETIME)
// Avatar: []byte -> 二进制数据 (BLOB)
```

---

## 实际应用示例

### 1. 自定义时间类型

```go
type CustomTime struct {
    time.Time
}

// 实现Scanner接口
func (ct *CustomTime) Scan(value interface{}) error {
    if value == nil {
        ct.Time = time.Time{}
        return nil
    }
    
    switch v := value.(type) {
    case time.Time:
        ct.Time = v
    case string:
        // 解析自定义时间格式
        t, err := time.Parse("2006-01-02 15:04:05", v)
        if err != nil {
            return err
        }
        ct.Time = t
    case []byte:
        t, err := time.Parse("2006-01-02 15:04:05", string(v))
        if err != nil {
            return err
        }
        ct.Time = t
    default:
        return fmt.Errorf("cannot scan %T into CustomTime", value)
    }
    return nil
}

// 实现Valuer接口
func (ct CustomTime) Value() (driver.Value, error) {
    if ct.Time.IsZero() {
        return nil, nil
    }
    return ct.Time, nil
}

// 使用
type Order struct {
    ID        uint       `gorm:"primaryKey"`
    CreatedAt CustomTime `gorm:"type:datetime"`
}
```

### 2. JSON类型处理

```go
type JSONData map[string]interface{}

// 实现Scanner接口
func (j *JSONData) Scan(value interface{}) error {
    if value == nil {
        *j = nil
        return nil
    }
    
    var data map[string]interface{}
    switch v := value.(type) {
    case string:
        err := json.Unmarshal([]byte(v), &data)
        if err != nil {
            return err
        }
    case []byte:
        err := json.Unmarshal(v, &data)
        if err != nil {
            return err
        }
    default:
        return fmt.Errorf("cannot scan %T into JSONData", value)
    }
    
    *j = data
    return nil
}

// 实现Valuer接口
func (j JSONData) Value() (driver.Value, error) {
    if j == nil {
        return nil, nil
    }
    
    data, err := json.Marshal(j)
    if err != nil {
        return nil, err
    }
    return string(data), nil
}

// 使用
type User struct {
    ID       uint     `gorm:"primaryKey"`
    Name     string   `gorm:"type:varchar(100)"`
    Settings JSONData `gorm:"type:json"`
}
```

### 3. 枚举类型处理

```go
type UserRole string

const (
    RoleAdmin UserRole = "admin"
    RoleUser  UserRole = "user"
    RoleGuest UserRole = "guest"
)

// 实现Scanner接口
func (r *UserRole) Scan(value interface{}) error {
    if value == nil {
        *r = ""
        return nil
    }
    
    switch v := value.(type) {
    case string:
        *r = UserRole(v)
    case []byte:
        *r = UserRole(v)
    default:
        return fmt.Errorf("cannot scan %T into UserRole", value)
    }
    return nil
}

// 实现Valuer接口
func (r UserRole) Value() (driver.Value, error) {
    if r == "" {
        return nil, nil
    }
    return string(r), nil
}

// 使用
type User struct {
    ID   uint     `gorm:"primaryKey"`
    Name string   `gorm:"type:varchar(100)"`
    Role UserRole `gorm:"type:enum('admin','user','guest')"`
}
```

---

## 常见问题和解决方案

### 1. 类型不匹配问题

#### 问题：数据库类型与Go类型不匹配
```go
// ❌ 错误：数据库是DECIMAL，Go是string
type Product struct {
    Price string `gorm:"column:price"`  // 应该是float64
}

// ✅ 正确：类型匹配
type Product struct {
    Price float64 `gorm:"type:decimal(10,2)"`  // 指定数据库类型
}
```

#### 解决方案：
```go
// 方案1：修改Go类型
type Product struct {
    Price float64 `gorm:"type:decimal(10,2)"`
}

// 方案2：使用自定义类型
type Price string

func (p *Price) Scan(value interface{}) error {
    // 自定义转换逻辑
}

func (p Price) Value() (driver.Value, error) {
    // 自定义转换逻辑
}
```

### 2. 字段映射问题

#### 问题：字段名不匹配
```go
// 数据库字段：user_name, created_at
// Go字段：UserName, CreatedAt
type User struct {
    ID        uint      `gorm:"primaryKey"`
    UserName  string    `gorm:"column:user_name"`     // 明确指定列名
    CreatedAt time.Time `gorm:"column:created_at"`    // 明确指定列名
}
```

### 3. NULL值处理

#### 问题：数据库NULL值处理
```go
// 使用指针类型处理NULL值
type User struct {
    ID       uint   `gorm:"primaryKey"`
    Name     string `gorm:"type:varchar(100)"`
    Email    *string `gorm:"type:varchar(255)"`  // 使用指针处理NULL
    Phone    *string `gorm:"type:varchar(20)"`   // 使用指针处理NULL
}
```

### 4. 精度丢失问题

#### 问题：浮点数精度丢失
```go
// ❌ 错误：使用float32可能丢失精度
type Product struct {
    Price float32 `gorm:"type:decimal(10,2)"`
}

// ✅ 正确：使用float64保持精度
type Product struct {
    Price float64 `gorm:"type:decimal(10,2)"`
}
```

---

## 最佳实践

### 1. 类型选择

```go
// ✅ 推荐：使用合适的Go类型
type User struct {
    ID        uint      `gorm:"primaryKey"`
    Name      string    `gorm:"type:varchar(100)"`
    Age       int       `gorm:"type:int"`
    Salary    float64   `gorm:"type:decimal(10,2)"`
    IsActive  bool      `gorm:"type:boolean"`
    CreatedAt time.Time `gorm:"type:datetime"`
}

// ❌ 不推荐：类型不匹配
type User struct {
    ID        string    `gorm:"primaryKey"`  // 主键应该是uint
    Name      int       `gorm:"type:varchar(100)"`  // 名称应该是string
    Age       string    `gorm:"type:int"`    // 年龄应该是int
}
```

### 2. 自定义类型实现

```go
// ✅ 推荐：完整实现Scanner和Valuer接口
type Status string

func (s *Status) Scan(value interface{}) error {
    // 处理所有可能的输入类型
    if value == nil {
        *s = ""
        return nil
    }
    
    switch v := value.(type) {
    case string:
        *s = Status(v)
    case []byte:
        *s = Status(v)
    default:
        return fmt.Errorf("cannot scan %T into Status", value)
    }
    return nil
}

func (s Status) Value() (driver.Value, error) {
    if s == "" {
        return nil, nil
    }
    return string(s), nil
}
```

### 3. 错误处理

```go
// ✅ 推荐：提供详细的错误信息
func (s *Status) Scan(value interface{}) error {
    if value == nil {
        *s = ""
        return nil
    }
    
    switch v := value.(type) {
    case string:
        *s = Status(v)
    case []byte:
        *s = Status(v)
    default:
        return fmt.Errorf("cannot scan %T into Status, expected string or []byte", value)
    }
    return nil
}
```

### 4. 测试验证

```go
// ✅ 推荐：编写测试验证类型转换
func TestStatusScan(t *testing.T) {
    var status Status
    
    // 测试字符串输入
    err := status.Scan("pending")
    assert.NoError(t, err)
    assert.Equal(t, StatusPending, status)
    
    // 测试字节数组输入
    err = status.Scan([]byte("completed"))
    assert.NoError(t, err)
    assert.Equal(t, StatusCompleted, status)
    
    // 测试nil输入
    err = status.Scan(nil)
    assert.NoError(t, err)
    assert.Equal(t, Status(""), status)
    
    // 测试错误输入
    err = status.Scan(123)
    assert.Error(t, err)
}
```

### 5. 性能考虑

```go
// ✅ 推荐：避免不必要的类型转换
type User struct {
    ID   uint   `gorm:"primaryKey"`
    Name string `gorm:"type:varchar(100)"`  // 直接使用string，无需自定义类型
}

// 只有在需要特殊处理时才使用自定义类型
type User struct {
    ID     uint     `gorm:"primaryKey"`
    Name   string   `gorm:"type:varchar(100)"`
    Status UserStatus `gorm:"type:enum('active','inactive')"`  // 需要特殊处理
}
```

---

## 总结

### 关键概念对比

| 概念 | 作用 | 使用时机 | 示例 |
|------|------|----------|------|
| **Scan接口** | 从数据库读取数据并转换为Go类型 | 查询操作时 | `func (s *Status) Scan(value interface{}) error` |
| **Value接口** | 将Go类型转换为数据库可存储格式 | 写入操作时 | `func (s Status) Value() (driver.Value, error)` |
| **type标签** | 定义数据库表结构 | AutoMigrate时 | `gorm:"type:varchar(100)"` |

### 使用原则

1. **默认转换**：对于基本类型，GORM提供默认转换，通常无需自定义
2. **自定义类型**：对于复杂类型或需要特殊处理的类型，实现Scan和Value接口
3. **类型匹配**：确保Go类型与数据库类型兼容
4. **错误处理**：在自定义类型转换中提供详细的错误信息
5. **测试验证**：编写测试确保类型转换的正确性

### 最佳实践

- 优先使用GORM的默认类型转换
- 只有在需要特殊处理时才实现自定义类型
- 确保类型转换的健壮性和错误处理
- 编写测试验证类型转换的正确性
- 考虑性能影响，避免不必要的类型转换

通过正确理解和使用这些概念，可以确保GORM与数据库之间的类型转换既安全又高效。

### 使用误区
- tag中gorm标签的type只是定义饵料数据库的类型，跟类型转换不是同一个维度的事情，不要混为一谈。 
