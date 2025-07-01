package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/stones-hub/taurus-pro-storage/pkg/db"
	"github.com/stones-hub/taurus-pro-storage/pkg/redisx"
	"gorm.io/gorm"
)

// User 用户模型
type User struct {
	ID        uint   `gorm:"primarykey"`
	Name      string `gorm:"size:100;not null"`
	Email     string `gorm:"size:100;uniqueIndex"`
	Age       int
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`
}

// Order 订单模型
type Order struct {
	ID        uint `gorm:"primarykey"`
	UserID    uint
	Amount    float64
	Status    string `gorm:"size:20"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`
}

func main() {
	// 第一部分：初始化数据库
	fmt.Println("\n=== 初始化数据库 ===")

	// MySQL 配置
	mysqlOpts := db.NewDbOptions(
		"mysql_db", // 数据库连接名称
		"mysql",    // 数据库类型
		"root:123456@tcp(127.0.0.1:3306)/test?charset=utf8mb4&parseTime=True&loc=Local", // DSN
		db.WithMaxOpenConns(10),
		db.WithMaxIdleConns(5),
		db.WithConnMaxLifetime(time.Hour),
		db.WithMaxRetries(3),
		db.WithRetryDelay(1),
	)

	// 初始化数据库连接
	if err := db.InitDB(mysqlOpts); err != nil {
		log.Fatalf("数据库初始化失败: %v", err)
	}

	// 获取数据库连接
	mysqlDB, err := db.GetDB("mysql_db")
	if err != nil {
		log.Fatalf("获取数据库连接失败: %v", err)
	}

	// 自动迁移表结构
	if err := mysqlDB.AutoMigrate(&User{}, &Order{}); err != nil {
		log.Fatalf("数据库迁移失败: %v", err)
	}

	// 第二部分：基本的 CRUD 操作
	fmt.Println("\n=== 基本的 CRUD 操作 ===")

	// 创建用户
	user := &User{
		Name:  "张三",
		Email: "zhangsan@example.com",
		Age:   25,
	}
	if err := db.Create("mysql_db", user); err != nil {
		log.Printf("创建用户失败: %v", err)
	} else {
		fmt.Printf("创建用户成功: ID = %d\n", user.ID)
	}

	// 查询用户
	var users []User
	if err := db.Find("mysql_db", &users, "age > ?", 20); err != nil {
		log.Printf("查询用户失败: %v", err)
	} else {
		fmt.Printf("查询到 %d 个用户\n", len(users))
		for _, u := range users {
			fmt.Printf("用户: %s, 邮箱: %s\n", u.Name, u.Email)
		}
	}

	// 更新用户
	user.Age = 26
	if err := db.Update("mysql_db", user); err != nil {
		log.Printf("更新用户失败: %v", err)
	} else {
		fmt.Println("更新用户成功")
	}

	// 第三部分：事务操作
	fmt.Println("\n=== 事务操作 ===")
	err = db.ExecuteInTransaction("mysql_db", func(tx *gorm.DB) error {
		// 创建订单
		order := &Order{
			UserID: user.ID,
			Amount: 100.50,
			Status: "pending",
		}
		if err := tx.Create(order).Error; err != nil {
			return fmt.Errorf("创建订单失败: %v", err)
		}

		// 模拟其他操作
		if order.Amount > 1000 {
			return fmt.Errorf("订单金额超过限制")
		}

		return nil
	})
	if err != nil {
		log.Printf("事务执行失败: %v", err)
	} else {
		fmt.Println("事务执行成功")
	}

	// 第四部分：分页查询
	fmt.Println("\n=== 分页查询 ===")
	err = db.PaginateQuery("mysql_db", "users", 10, func(records []map[string]interface{}) error {
		for _, record := range records {
			fmt.Printf("用户记录: %v\n", record)
		}
		return nil
	})
	if err != nil {
		log.Printf("分页查询失败: %v", err)
	}

	// 第五部分：原生 SQL 查询
	fmt.Println("\n=== 原生 SQL 查询 ===")
	var result []struct {
		Name  string
		Count int
	}
	err = db.QuerySQL("mysql_db", &result, `
		SELECT u.name, COUNT(o.id) as count 
		FROM users u 
		LEFT JOIN orders o ON u.id = o.user_id 
		GROUP BY u.id
	`)
	if err != nil {
		log.Printf("SQL 查询失败: %v", err)
	} else {
		for _, r := range result {
			fmt.Printf("用户 %s 有 %d 个订单\n", r.Name, r.Count)
		}
	}

	// 第六部分：Redis 缓存示例
	fmt.Println("\n=== Redis 缓存示例 ===")

	// 初始化 Redis
	err = redisx.InitRedis(
		redisx.WithAddrs("localhost:6379"),
		redisx.WithPassword(""),
		redisx.WithDB(0),
	)
	if err != nil {
		log.Printf("Redis 初始化失败: %v", err)
	} else {
		defer redisx.Redis.Close()

		// 使用 Redis 缓存用户信息
		ctx := context.Background()
		cacheKey := fmt.Sprintf("user:%d", user.ID)

		err = redisx.Redis.HSet(ctx, cacheKey,
			"name", user.Name,
			"email", user.Email,
			"age", user.Age,
		)
		if err != nil {
			log.Printf("缓存用户信息失败: %v", err)
		}

		// 从缓存获取用户信息
		if values, err := redisx.Redis.HGetList(ctx, cacheKey); err != nil {
			log.Printf("获取缓存失败: %v", err)
		} else {
			fmt.Println("从缓存获取的用户信息:", values)
		}
	}

	fmt.Println("\n=== 示例运行完成 ===")
}
