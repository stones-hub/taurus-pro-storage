package main

import (
	"fmt"
	"log"
	"time"

	"github.com/stones-hub/taurus-pro-storage/pkg/db"
)

// User 用户模型
type User struct {
	ID        uint      `gorm:"primarykey"`
	Name      string    `gorm:"type:String"`
	Email     string    `gorm:"type:String"`
	Age       int       `gorm:"type:Int32"`
	CreatedAt time.Time `gorm:"type:DateTime"`
	UpdatedAt time.Time `gorm:"type:DateTime"`
}

func main() {
	// 初始化 ClickHouse 数据库连接
	err := db.InitDB(
		db.WithDBName("clickhouse_db"),
		db.WithDBType("clickhouse"),
		db.WithDSN("clickhouse://default:@localhost:9000/default?dial_timeout=10s&max_execution_time=60"),
		db.WithMaxOpenConns(10),
		db.WithMaxIdleConns(5),
	)
	if err != nil {
		log.Fatalf("Failed to initialize ClickHouse: %v", err)
	}

	// 获取数据库连接
	clickhouseDB, err := db.GetDB("clickhouse_db")
	if err != nil {
		log.Fatalf("Failed to get ClickHouse connection: %v", err)
	}

	// 自动迁移表结构
	err = clickhouseDB.AutoMigrate(&User{})
	if err != nil {
		log.Printf("AutoMigrate warning: %v", err)
	}

	// 创建用户
	user := User{
		Name:      "张三",
		Email:     "zhangsan@example.com",
		Age:       25,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	err = db.Create("clickhouse_db", &user)
	if err != nil {
		log.Printf("Failed to create user: %v", err)
	} else {
		fmt.Printf("User created with ID: %d\n", user.ID)
	}

	// 查询用户
	var users []User
	err = db.Find("clickhouse_db", &users, "age > ?", 20)
	if err != nil {
		log.Printf("Failed to find users: %v", err)
	} else {
		fmt.Printf("Found %d users:\n", len(users))
		for _, u := range users {
			fmt.Printf("  ID: %d, Name: %s, Email: %s, Age: %d\n", u.ID, u.Name, u.Email, u.Age)
		}
	}

	// 使用原生 SQL 查询
	var count int64
	err = db.QuerySQL("clickhouse_db", &count, "SELECT count() FROM users WHERE age > ?", 20)
	if err != nil {
		log.Printf("Failed to execute SQL: %v", err)
	} else {
		fmt.Printf("Total users with age > 20: %d\n", count)
	}

	// 分页查询
	err = db.Paginate("clickhouse_db", &users, 1, 10, "age > ?", 20)
	if err != nil {
		log.Printf("Failed to paginate users: %v", err)
	} else {
		fmt.Printf("Page 1 results: %d users\n", len(users))
	}

	fmt.Println("ClickHouse example completed successfully!")
}
