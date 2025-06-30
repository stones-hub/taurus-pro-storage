package db

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 测试用的数据结构
type TestUser struct {
	ID        uint `gorm:"primarykey"`
	Name      string
	Age       int
	CreatedAt time.Time
	UpdatedAt time.Time
}

func setupTestDB(t *testing.T) (string, func()) {
	// 创建临时SQLite数据库文件
	tmpDir, err := os.MkdirTemp("", "test-db-*")
	assert.NoError(t, err)
	dbPath := filepath.Join(tmpDir, "test.db")

	// 初始化数据库连接
	InitDB("test_db", "sqlite", dbPath, logger.Default, 3, 1)

	// 自动迁移测试表
	db := getDB("test_db")
	err = db.AutoMigrate(&TestUser{})
	assert.NoError(t, err)

	// 返回清理函数
	cleanup := func() {
		CloseDB()
		os.RemoveAll(tmpDir)
	}

	return dbPath, cleanup
}

func TestInitDB(t *testing.T) {
	dbPath, cleanup := setupTestDB(t)
	defer cleanup()

	// 验证数据库连接是否成功
	assert.FileExists(t, dbPath)
	assert.NotNil(t, dbConnections["test_db"])
}

func TestCRUD(t *testing.T) {
	_, cleanup := setupTestDB(t)
	defer cleanup()

	// 测试创建记录
	t.Run("Create", func(t *testing.T) {
		user := TestUser{Name: "张三", Age: 25}
		err := Create("test_db", &user)
		assert.NoError(t, err)
		assert.NotZero(t, user.ID)
	})

	// 测试查询记录
	t.Run("Find", func(t *testing.T) {
		var users []TestUser
		err := Find("test_db", &users)
		assert.NoError(t, err)
		assert.Len(t, users, 1)
		assert.Equal(t, "张三", users[0].Name)
	})

	// 测试更新记录
	t.Run("Update", func(t *testing.T) {
		var user TestUser
		Find("test_db", &user, "name = ?", "张三")
		user.Age = 26
		err := Update("test_db", &user)
		assert.NoError(t, err)

		var updatedUser TestUser
		Find("test_db", &updatedUser, user.ID)
		assert.Equal(t, 26, updatedUser.Age)
	})

	// 测试删除记录
	t.Run("Delete", func(t *testing.T) {
		var user TestUser
		Find("test_db", &user, "name = ?", "张三")
		err := Delete("test_db", &user)
		assert.NoError(t, err)

		var users []TestUser
		Find("test_db", &users)
		assert.Empty(t, users)
	})
}

func TestTransaction(t *testing.T) {
	_, cleanup := setupTestDB(t)
	defer cleanup()

	t.Run("Successful Transaction", func(t *testing.T) {
		err := ExecuteInTransaction("test_db", func(tx *gorm.DB) error {
			user1 := TestUser{Name: "李四", Age: 30}
			user2 := TestUser{Name: "王五", Age: 35}

			if err := tx.Create(&user1).Error; err != nil {
				return err
			}
			if err := tx.Create(&user2).Error; err != nil {
				return err
			}
			return nil
		})

		assert.NoError(t, err)
		var users []TestUser
		Find("test_db", &users)
		assert.Len(t, users, 2)
	})

	t.Run("Failed Transaction", func(t *testing.T) {
		err := ExecuteInTransaction("test_db", func(tx *gorm.DB) error {
			user := TestUser{Name: "赵六", Age: 40}
			if err := tx.Create(&user).Error; err != nil {
				return err
			}
			return errors.New("模拟错误")
		})

		assert.Error(t, err)
		var users []TestUser
		Find("test_db", &users)
		assert.Len(t, users, 2) // 事务回滚，数据数量应该保持不变
	})
}

func TestPagination(t *testing.T) {
	_, cleanup := setupTestDB(t)
	defer cleanup()

	// 准备测试数据
	for i := 0; i < 15; i++ {
		user := TestUser{
			Name: fmt.Sprintf("用户%d", i),
			Age:  20 + i,
		}
		Create("test_db", &user)
	}

	t.Run("PaginateQuery", func(t *testing.T) {
		var count int
		err := PaginateQuery("test_db", "test_users", 5, func(records []map[string]interface{}) error {
			count += len(records)
			return nil
		})

		assert.NoError(t, err)
		assert.Equal(t, 15, count)
	})

	t.Run("Paginate", func(t *testing.T) {
		var users []TestUser
		err := Paginate("test_db", &users, 1, 10)
		assert.NoError(t, err)
		assert.Len(t, users, 10)

		err = Paginate("test_db", &users, 2, 10)
		assert.NoError(t, err)
		assert.Len(t, users, 5)
	})
}

func TestCustomLogger(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "test-db-log-*")
	assert.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	logPath := filepath.Join(tmpDir, "test.log")

	// 确保日志目录存在并有正确的权限
	err = os.MkdirAll(filepath.Dir(logPath), 0755)
	assert.NoError(t, err)

	// 创建空的日志文件
	_, err = os.Create(logPath)
	assert.NoError(t, err)

	config := DBLoggerConfig{
		LogFilePath:   logPath,
		MaxSize:       1,
		MaxBackups:    1,
		MaxAge:        1,
		Compress:      false,
		LogLevel:      logger.Info,
		SlowThreshold: time.Second,
	}

	logger := NewDBCustomLogger(config)
	assert.NotNil(t, logger)

	// 初始化数据库并执行一些操作以生成日志
	dbPath := filepath.Join(tmpDir, "test.db")
	InitDB("test_db_log", "sqlite", dbPath, logger, 3, 1)
	defer CloseDB()

	// 自动迁移测试表
	db := getDB("test_db_log")
	err = db.AutoMigrate(&TestUser{})
	assert.NoError(t, err)

	// 执行一些数据库操作以生成日志
	user := TestUser{Name: "测试用户", Age: 25}
	err = Create("test_db_log", &user)
	assert.NoError(t, err)

	// 验证日志文件是否创建并包含内容
	assert.FileExists(t, logPath)

	// 读取日志文件内容
	logContent, err := os.ReadFile(logPath)
	assert.NoError(t, err)
	assert.NotEmpty(t, logContent)
}
