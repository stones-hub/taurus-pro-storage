// Copyright (c) 2025 Taurus Team. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Author: yelei
// Email: 61647649@qq.com
// Date: 2025-08-06

package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/stones-hub/taurus-pro-storage/pkg/db"
	"github.com/stones-hub/taurus-pro-storage/pkg/db/dao"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

var once sync.Once

// User 用户实体
type User struct {
	ID        uint      `gorm:"primarykey" json:"id"`
	Name      string    `gorm:"column:username;type:varchar(100);not null;comment:用户名" json:"name"`
	Password  string    `gorm:"column:password_hash;type:varchar(255);not null;comment:密码" json:"-"`
	CreatedAt time.Time `gorm:"column:created_at;autoCreateTime;comment:创建时间" json:"created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at;autoUpdateTime;comment:更新时间" json:"updated_at"`
}

// TableName 实现Entity接口 - 返回数据库表名
func (u User) TableName() string {
	return "users"
}

// DB 实现Entity接口 - 返回数据库连接实例
func (u User) DB() *gorm.DB {
	once.Do(func() {
		err := db.InitDB(
			db.WithDBName("mysql_db"),
			db.WithDBType("mysql"),
			db.WithDSN("apps:apps@tcp(127.0.0.1:3306)/sys?charset=utf8mb4&parseTime=True&loc=Local"),
			db.WithMaxOpenConns(10),
			db.WithMaxIdleConns(5),
		)
		if err != nil {
			log.Fatalf("Failed to initialize database: %v", err)
		}
	})
	return db.DbList()["mysql_db"]
}

type UserRepository struct {
	dao.Repository[User]
}

func NewUserRepositoryWithDB(db *gorm.DB) *UserRepository {
	return &UserRepository{
		Repository: dao.NewBaseRepository[User](db),
	}
}

// NewUserRepository 创建User Repository实例
func NewUserRepository() (*UserRepository, error) {
	repo, err := dao.NewBaseRepositoryWithDB[User]()
	if err != nil {
		return nil, err
	}
	return &UserRepository{
		Repository: repo,
	}, nil
}

// 便捷方法 - 根据User结构体的实际字段定义
// FindByName 根据用户名查找用户
func (r *UserRepository) FindByName(ctx context.Context, name string) (*User, error) {
	return r.FindOneByCondition(ctx, "username = ?", name)
}

// FindByNameLike 根据用户名模糊查找用户
func (r *UserRepository) FindByNameLike(ctx context.Context, namePattern string) ([]User, error) {
	return r.FindByCondition(ctx, "username LIKE ?", "%"+namePattern+"%")
}

// FindByCreatedTimeRange 根据创建时间范围查找用户
func (r *UserRepository) FindByCreatedTimeRange(ctx context.Context, startTime, endTime time.Time) ([]User, error) {
	return r.FindByCondition(ctx, "created_at BETWEEN ? AND ?", startTime, endTime)
}

// ==================== GORM钩子函数示例 ====================
// 这些钩子函数会在不同的数据库操作阶段自动调用

// BeforeCreate 创建记录前的钩子
func (u *User) BeforeCreate(tx *gorm.DB) error {
	log.Println("User BeforeCreate: 准备创建用户")

	// 数据验证
	if u.Name == "" {
		return fmt.Errorf("用户名不能为空")
	}
	if u.Password == "" {
		return fmt.Errorf("密码不能为空")
	}

	// 密码加密（如果还没有加密）
	if !strings.HasPrefix(u.Password, "$2a$") {
		hashedPassword, err := bcrypt.GenerateFromPassword([]byte(u.Password), bcrypt.DefaultCost)
		if err != nil {
			return fmt.Errorf("密码加密失败: %v", err)
		}
		u.Password = string(hashedPassword)
	}

	// 设置默认值
	if u.CreatedAt.IsZero() {
		u.CreatedAt = time.Now()
	}
	if u.UpdatedAt.IsZero() {
		u.UpdatedAt = time.Now()
	}

	log.Println("User BeforeCreate: 用户数据验证和预处理完成")
	return nil
}

// AfterCreate 创建记录后的钩子
func (u *User) AfterCreate(tx *gorm.DB) error {
	log.Printf("User AfterCreate: 用户 %s 创建成功，ID: %d", u.Name, u.ID)

	// 可以在这里执行一些后续操作
	// 比如：发送欢迎邮件、记录审计日志等

	return nil
}

// BeforeUpdate 更新记录前的钩子
func (u *User) BeforeUpdate(tx *gorm.DB) error {
	log.Println("User BeforeUpdate: 准备更新用户")

	// 数据验证
	if u.Name == "" {
		return fmt.Errorf("用户名不能为空")
	}

	// 更新修改时间
	u.UpdatedAt = time.Now()

	// 如果密码被修改，重新加密
	if tx.Statement.Changed("Password") {
		if !strings.HasPrefix(u.Password, "$2a$") {
			hashedPassword, err := bcrypt.GenerateFromPassword([]byte(u.Password), bcrypt.DefaultCost)
			if err != nil {
				return fmt.Errorf("密码加密失败: %v", err)
			}
			u.Password = string(hashedPassword)
		}
	}

	log.Println("User BeforeUpdate: 用户数据更新预处理完成")
	return nil
}

// AfterUpdate 更新记录后的钩子
func (u *User) AfterUpdate(tx *gorm.DB) error {
	log.Printf("User AfterUpdate: 用户 %s 更新成功", u.Name)

	// 可以在这里执行一些后续操作
	// 比如：记录审计日志、发送通知等

	return nil
}

// BeforeDelete 删除记录前的钩子
func (u *User) BeforeDelete(tx *gorm.DB) error {
	log.Printf("User BeforeDelete: 准备删除用户 %s (ID: %d)", u.Name, u.ID)

	// 检查是否可以删除
	if u.ID == 1 {
		return fmt.Errorf("不能删除超级管理员用户")
	}

	// 可以在这里执行软删除逻辑
	// 比如：将用户状态标记为已删除，而不是真正删除记录

	log.Println("User BeforeDelete: 用户删除验证完成")
	return nil
}

// AfterDelete 删除记录后的钩子
func (u *User) AfterDelete(tx *gorm.DB) error {
	log.Printf("User AfterDelete: 用户 %s 删除成功", u.Name)

	// 可以在这里执行一些后续操作
	// 比如：记录审计日志、清理相关数据等

	return nil
}

// BeforeSave 保存记录前的钩子（创建和更新都会调用）
func (u *User) BeforeSave(tx *gorm.DB) error {
	log.Println("User BeforeSave: 准备保存用户")

	// 通用的保存前逻辑
	// 比如：数据清理、格式验证等

	// 清理用户名前后空格
	u.Name = strings.TrimSpace(u.Name)

	// 用户名长度验证
	if len(u.Name) < 2 || len(u.Name) > 50 {
		return fmt.Errorf("用户名长度必须在2-50个字符之间")
	}

	log.Println("User BeforeSave: 用户数据保存预处理完成")
	return nil
}

// AfterSave 保存记录后的钩子（创建和更新都会调用）
func (u *User) AfterSave(tx *gorm.DB) error {
	log.Printf("User AfterSave: 用户 %s 保存成功", u.Name)

	// 通用的保存后逻辑
	// 比如：缓存更新、事件触发等

	return nil
}

// BeforeFind 查询记录前的钩子
func (u *User) BeforeFind(tx *gorm.DB) error {
	log.Println("User BeforeFind: 准备查询用户")

	// 可以在这里添加一些查询前的逻辑
	// 比如：权限检查、查询条件预处理等

	return nil
}

// AfterFind 查询记录后的钩子
func (u *User) AfterFind(tx *gorm.DB) error {
	log.Println("User AfterFind: 用户查询完成")

	// 可以在这里执行一些查询后的逻辑
	// 比如：数据脱敏、字段计算等

	// 示例：隐藏敏感信息
	u.Password = "***"

	return nil
}

// BeforeUpsert 插入或更新记录前的钩子
func (u *User) BeforeUpsert(tx *gorm.DB) error {
	log.Println("User BeforeUpsert: 准备插入或更新用户")

	// 可以在这里执行一些通用的逻辑
	// 比如：数据验证、默认值设置等

	return nil
}

// AfterUpsert 插入或更新记录后的钩子
func (u *User) AfterUpsert(tx *gorm.DB) error {
	log.Println("User AfterUpsert: 用户插入或更新完成")

	// 可以在这里执行一些后续操作

	return nil
}

func main() {
	user := User{Name: "张三", Password: "123456"}
	userRepo, err := NewUserRepository()
	if err != nil {
		log.Fatalf("Failed to create user repository: %v", err)
	}
	userRepo.Create(context.Background(), &user)
}
