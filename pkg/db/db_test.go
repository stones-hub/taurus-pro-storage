package db

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// TestUser 测试用的模型结构
type TestUser struct {
	ID   uint   `gorm:"primarykey"`
	Name string `gorm:"size:255"`
	Age  int
}

func TestMain(m *testing.M) {
	// 运行测试前的设置
	setup()
	// 运行测试
	code := m.Run()
	// 运行测试后的清理
	teardown()
	// 退出
	os.Exit(code)
}

func setup() {
	// 清理可能存在的测试数据库文件
	os.Remove("./testdata/test.db")
	os.Remove("./testdata/test.log")
	// 确保测试目录存在
	os.MkdirAll("./testdata", 0755)
}

func teardown() {
	// 清理测试数据
	CloseDB()
	os.Remove("./testdata/test.db")
	os.Remove("./testdata/test.log")
}

// 清理数据库表
func cleanupTable(t *testing.T, dbName string) {
	db, err := GetDB(dbName)
	assert.NoError(t, err)
	db.Exec("DELETE FROM test_users")
}

func TestLogger(t *testing.T) {
	// 测试默认日志配置
	logger := &DefaultLogger{
		FilePath:      "./testdata/test.log",
		MaxSize:       1,
		MaxBackups:    1,
		MaxAge:        1,
		Compress:      false,
		Level:         logger.Info,
		SlowThreshold: time.Second,
	}

	// 测试日志创建
	l, err := logger.Build()
	assert.NoError(t, err)
	assert.NotNil(t, l)

	// 测试日志注册
	err = RegisterLogger("test_logger", logger)
	assert.NoError(t, err)

	// 测试重复注册
	err = RegisterLogger("test_logger", logger)
	assert.Error(t, err)

	// 测试获取日志
	l2 := GetLogger("test_logger")
	assert.NotNil(t, l2)

	// 测试获取不存在的日志（应返回默认日志）
	l3 := GetLogger("non_existent")
	assert.NotNil(t, l3)
}

func TestDatabaseConnection(t *testing.T) {
	// 创建测试数据库配置
	dbOpt := NewDbOptions(
		"test_db",
		"sqlite",
		"./testdata/test.db",
		WithMaxOpenConns(1),
		WithMaxIdleConns(1),
		WithConnMaxLifetime(time.Hour),
		WithMaxRetries(1),
		WithRetryDelay(1),
		WithLoggerName("test_logger"),
	)

	// 测试数据库初始化
	err := InitDB(dbOpt)
	assert.NoError(t, err)

	// 测试获取数据库连接
	db, err := GetDB("test_db")
	assert.NoError(t, err)
	assert.NotNil(t, db)

	// 测试获取不存在的数据库
	_, err = GetDB("non_existent")
	assert.Error(t, err)

	// 测试数据库列表
	dbs := DbList()
	assert.Equal(t, 1, len(dbs))

	// 测试自动迁移
	err = db.AutoMigrate(&TestUser{})
	assert.NoError(t, err)
}

func TestCRUD(t *testing.T) {
	cleanupTable(t, "test_db")

	// 准备测试数据
	user := &TestUser{
		Name: "test_user",
		Age:  25,
	}

	// 测试创建
	err := Create("test_db", user)
	assert.NoError(t, err)
	assert.NotEqual(t, 0, user.ID)

	// 测试查询
	var found TestUser
	err = Find("test_db", &found, "id = ?", user.ID)
	assert.NoError(t, err)
	assert.Equal(t, user.Name, found.Name)

	// 测试更新
	user.Name = "updated_user"
	err = Update("test_db", user)
	assert.NoError(t, err)

	// 验证更新
	err = Find("test_db", &found, "id = ?", user.ID)
	assert.NoError(t, err)
	assert.Equal(t, "updated_user", found.Name)

	// 测试删除
	err = Delete("test_db", user)
	assert.NoError(t, err)

	// 验证删除
	var count int64
	db, _ := GetDB("test_db")
	db.Model(&TestUser{}).Where("id = ?", user.ID).Count(&count)
	assert.Equal(t, int64(0), count)
}

func TestTransaction(t *testing.T) {
	cleanupTable(t, "test_db")

	// 测试成功的事务
	err := ExecuteInTransaction("test_db", func(tx *gorm.DB) error {
		user := &TestUser{Name: "tx_user", Age: 30}
		return tx.Create(user).Error
	})
	assert.NoError(t, err)

	// 测试失败的事务
	err = ExecuteInTransaction("test_db", func(tx *gorm.DB) error {
		user := &TestUser{Name: "tx_user2", Age: 31}
		if err := tx.Create(user).Error; err != nil {
			return err
		}
		return fmt.Errorf("rollback test")
	})
	assert.Error(t, err)

	// 验证事务回滚
	var count int64
	db, _ := GetDB("test_db")
	db.Model(&TestUser{}).Where("name = ?", "tx_user2").Count(&count)
	assert.Equal(t, int64(0), count)
}

func TestPagination(t *testing.T) {
	cleanupTable(t, "test_db")

	// 准备测试数据
	for i := 0; i < 10; i++ {
		user := &TestUser{
			Name: fmt.Sprintf("user_%d", i),
			Age:  20 + i,
		}
		err := Create("test_db", user)
		assert.NoError(t, err)
	}

	// 测试分页查询
	var users []TestUser
	err := Paginate("test_db", &users, 1, 5)
	assert.NoError(t, err)
	assert.Equal(t, 5, len(users))

	// 测试带处理函数的分页查询
	count := 0
	err = PaginateQuery("test_db", "test_users", 3, func(records []map[string]interface{}) error {
		count += len(records)
		return nil
	})
	assert.NoError(t, err)
	assert.Equal(t, 10, count)

	// 验证总记录数
	var totalCount int64
	db, _ := GetDB("test_db")
	db.Model(&TestUser{}).Count(&totalCount)
	assert.Equal(t, int64(10), totalCount)
}

func TestSQLExecution(t *testing.T) {
	cleanupTable(t, "test_db")

	// 准备测试数据
	for i := 0; i < 5; i++ {
		user := &TestUser{
			Name: fmt.Sprintf("sql_user_%d", i),
			Age:  20 + i,
		}
		Create("test_db", user)
	}

	// 测试执行原生SQL
	err := ExecSQL("test_db", "UPDATE test_users SET age = age + 1 WHERE age < ?", 25)
	assert.NoError(t, err)

	// 测试查询原生SQL
	var results []map[string]interface{}
	err = QuerySQL("test_db", &results, "SELECT * FROM test_users WHERE age > ?", 25)
	assert.NoError(t, err)
}

func TestDatabaseOptions(t *testing.T) {
	// 测试数据库选项
	opt := NewDbOptions(
		"test_db2",
		"sqlite",
		"./testdata/test2.db",
		WithMaxOpenConns(10),
		WithMaxIdleConns(5),
		WithConnMaxLifetime(time.Hour),
		WithMaxRetries(3),
		WithRetryDelay(1),
		WithLoggerName("test_logger"),
	)

	assert.Equal(t, "test_db2", opt.DBName)
	assert.Equal(t, 10, opt.MaxOpenConns)
	assert.Equal(t, 5, opt.MaxIdleConns)
	assert.Equal(t, time.Hour, opt.ConnMaxLifetime)
	assert.Equal(t, 3, opt.MaxRetries)
	assert.Equal(t, 1, opt.RetryDelay)
	assert.Equal(t, "test_logger", opt.LoggerName)
}
