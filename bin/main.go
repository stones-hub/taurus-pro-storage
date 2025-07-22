package main

import (
	"context"
	"log"
	"time"

	"github.com/stones-hub/taurus-pro-storage/pkg/db"
	"github.com/stones-hub/taurus-pro-storage/pkg/redisx"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// User 用户模型
type User struct {
	ID   uint   `gorm:"primarykey"`
	Name string `gorm:"size:255;not null"`
	Age  int    `gorm:"not null"`
}

func main() {
	log.Println("=== Taurus Pro Storage 示例程序 ===")

	// 1. 初始化Redis（使用JSON格式日志）
	log.Println("\n1. 初始化Redis...")

	// 创建Redis日志记录器
	redisLogger, err := redisx.NewRedisLogger(
		redisx.WithLogFilePath("./logs/redis/redis.log"),
		redisx.WithLogLevel(redisx.LogLevelInfo),
		redisx.WithLogFormatter(redisx.JSONLogFormatter),
	)
	if err != nil {
		log.Fatalf("创建Redis日志记录器失败: %v", err)
	}

	if err := redisx.InitRedis(
		redisx.WithAddrs("127.0.0.1:6379"),
		redisx.WithPassword(""),
		redisx.WithDB(0),
		redisx.WithPoolSize(10),
		redisx.WithMinIdleConns(5),
		redisx.WithTimeout(5*time.Second, 3*time.Second, 3*time.Second),
		redisx.WithMaxRetries(3),
		redisx.WithLogging(redisLogger),
	); err != nil {
		log.Fatalf("Redis初始化失败: %v", err)
	}

	// 测试Redis操作
	ctx := context.Background()
	redisx.Redis.Set(ctx, "test_key", "test_value", time.Minute)
	value, _ := redisx.Redis.Get(ctx, "test_key")
	log.Printf("Redis测试: test_key = %s", value)

	// 2. 初始化数据库连接（使用默认日志记录器）
	log.Println("\n2. 初始化数据库连接（默认日志）...")
	if err := db.InitDB(
		db.WithDBName("mysql_db"),
		db.WithDBType("mysql"),
		db.WithDSN("apps:apps@tcp(127.0.0.1:3306)/sys?charset=utf8mb4&parseTime=True&loc=Local"),
		db.WithMaxOpenConns(10),
		db.WithMaxIdleConns(5),
		db.WithConnMaxLifetime(time.Hour),
		db.WithMaxRetries(3),
		db.WithRetryDelay(1),
		db.WithLogger(nil), // 使用默认日志记录器
	); err != nil {
		log.Printf("数据库初始化失败: %v", err)
	}

	// 3. 初始化数据库连接（使用JSON格式日志记录器）
	log.Println("\n3. 初始化数据库连接（JSON格式日志）...")
	jsonLogger := db.NewDbLogger(
		db.WithLogFilePath("./logs/db/db.json.log"),
		db.WithLogLevel(logger.Info),
		db.WithLogFormatter(db.JSONLogFormatter),
	)

	if err := db.InitDB(
		db.WithDBName("mysql_json_db"),
		db.WithDBType("mysql"),
		db.WithDSN("apps:apps@tcp(127.0.0.1:3306)/sys?charset=utf8mb4&parseTime=True&loc=Local"),
		db.WithMaxOpenConns(10),
		db.WithMaxIdleConns(5),
		db.WithConnMaxLifetime(time.Hour),
		db.WithMaxRetries(3),
		db.WithRetryDelay(1),
		db.WithLogger(jsonLogger), // 使用JSON格式日志记录器
	); err != nil {
		log.Printf("数据库初始化失败: %v", err)
	}

	// 4. 初始化数据库连接（使用简单格式日志记录器）
	log.Println("\n4. 初始化数据库连接（简单格式日志）...")
	simpleLogger := db.NewDbLogger(
		db.WithLogFilePath("./logs/db/db.simple.log"),
		db.WithLogLevel(logger.Warn), // 只记录警告和错误
		db.WithLogFormatter(func(level logger.LogLevel, message string) string {
			return db.LogLevelToString(level) + ": " + message
		}),
	)

	if err := db.InitDB(
		db.WithDBName("mysql_simple_db"),
		db.WithDBType("mysql"),
		db.WithDSN("apps:apps@tcp(127.0.0.1:3306)/sys?charset=utf8mb4&parseTime=True&loc=Local"),
		db.WithMaxOpenConns(10),
		db.WithMaxIdleConns(5),
		db.WithConnMaxLifetime(time.Hour),
		db.WithMaxRetries(3),
		db.WithRetryDelay(1),
		db.WithLogger(simpleLogger), // 使用简单格式日志记录器
	); err != nil {
		log.Printf("数据库初始化失败: %v", err)
	}

	// 5. 初始化数据库连接（使用自定义格式化器）
	log.Println("\n5. 初始化数据库连接（自定义格式日志）...")
	customFormatter := func(level logger.LogLevel, message string) string {
		return db.LogLevelToString(level) + " | " + message + " | " + time.Now().Format("2006-01-02 15:04:05")
	}

	customLogger := db.NewDbLogger(
		db.WithLogFilePath("./logs/db/db.custom.log"),
		db.WithLogLevel(logger.Info),
		db.WithLogFormatter(customFormatter),
	)

	if err := db.InitDB(
		db.WithDBName("mysql_custom_db"),
		db.WithDBType("mysql"),
		db.WithDSN("apps:apps@tcp(127.0.0.1:3306)/sys?charset=utf8mb4&parseTime=True&loc=Local"),
		db.WithMaxOpenConns(10),
		db.WithMaxIdleConns(5),
		db.WithConnMaxLifetime(time.Hour),
		db.WithMaxRetries(3),
		db.WithRetryDelay(1),
		db.WithLogger(customLogger), // 使用自定义格式日志记录器
	); err != nil {
		log.Printf("数据库初始化失败: %v", err)
	}

	// 6. 测试数据库操作
	log.Println("\n6. 测试数据库操作...")

	// 获取数据库连接
	mysqlDB, err := db.GetDB("mysql_db")
	if err != nil {
		log.Printf("获取数据库连接失败: %v", err)
	} else {
		// 自动迁移表结构
		mysqlDB.AutoMigrate(&User{})

		// 创建用户
		user := User{Name: "张三", Age: 25}
		if err := db.Create("mysql_db", &user); err != nil {
			log.Printf("创建用户失败: %v", err)
		} else {
			log.Printf("创建用户成功: ID=%d, Name=%s, Age=%d", user.ID, user.Name, user.Age)
		}

		// 查询用户
		var users []User
		if err := db.Find("mysql_db", &users, "age > ?", 20); err != nil {
			log.Printf("查询用户失败: %v", err)
		} else {
			log.Printf("查询到 %d 个用户", len(users))
		}

		// 更新用户
		if err := db.Update("mysql_db", &User{ID: 1, Name: "李四", Age: 30}); err != nil {
			log.Printf("更新用户失败: %v", err)
		} else {
			log.Printf("更新用户成功")
		}

		// 删除用户
		if err := db.Delete("mysql_db", &User{ID: 1}); err != nil {
			log.Printf("删除用户失败: %v", err)
		} else {
			log.Printf("删除用户成功")
		}
	}

	// 7. 测试事务操作
	log.Println("\n7. 测试事务操作...")
	err = db.ExecuteInTransaction("mysql_db", func(tx *gorm.DB) error {
		// 在事务中创建用户
		user1 := User{Name: "王五", Age: 28}
		if err := tx.Create(&user1).Error; err != nil {
			return err
		}

		user2 := User{Name: "赵六", Age: 32}
		if err := tx.Create(&user2).Error; err != nil {
			return err
		}

		log.Printf("事务中创建了两个用户: ID=%d, ID=%d", user1.ID, user2.ID)
		return nil
	})

	if err != nil {
		log.Printf("事务执行失败: %v", err)
	} else {
		log.Printf("事务执行成功")
	}

	// 8. 测试分页查询
	log.Println("\n8. 测试分页查询...")
	var pageUsers []User
	if err := db.Paginate("mysql_db", &pageUsers, 1, 10, "age > ?", 20); err != nil {
		log.Printf("分页查询失败: %v", err)
	} else {
		log.Printf("分页查询成功，返回 %d 条记录", len(pageUsers))
	}

	// 9. 显示所有数据库连接
	log.Println("\n9. 当前数据库连接列表:")
	dbList := db.DbList()
	for name, conn := range dbList {
		log.Printf("  - %s: %v", name, conn)
	}

	// 10. 关闭数据库连接
	log.Println("\n10. 关闭数据库连接...")
	db.CloseDB()

	log.Println("\n=== 示例程序执行完成 ===")
	log.Println("请查看以下日志文件:")
	log.Println("  - ./logs/redis/redis.log (Redis JSON格式)")
	log.Println("  - ./logs/db/db.json.log (数据库JSON格式)")
	log.Println("  - ./logs/db/db.simple.log (数据库简单格式)")
	log.Println("  - ./logs/db/db.custom.log (数据库自定义格式)")
}
