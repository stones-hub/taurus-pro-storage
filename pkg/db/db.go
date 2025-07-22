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
// Date: 2025-06-13

package db

import (
	"fmt"
	"log"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// DBConnections stores multiple database connections
var dbConnections = make(map[string]*gorm.DB)

// Options 数据库配置选项
type Options struct {
	DBName          string           // 数据库连接名称
	DBType          string           // 数据库类型：postgres, mysql, sqlite
	DSN             string           // 数据库连接字符串
	MaxOpenConns    int              // 最大打开连接数，默认 25
	MaxIdleConns    int              // 最大空闲连接数，默认 25
	ConnMaxLifetime time.Duration    // 连接最大生命周期，默认 5 分钟
	MaxRetries      int              // 连接重试次数，默认 3
	RetryDelay      int              // 重试延迟（秒），默认 1
	Logger          logger.Interface // GORM日志记录器，如果为nil则使用默认
}

type Option func(*Options)

// WithMaxOpenConns 设置最大打开连接数
func WithMaxOpenConns(n int) Option {
	return func(o *Options) {
		o.MaxOpenConns = n
	}
}

// WithMaxIdleConns 设置最大空闲连接数
func WithMaxIdleConns(n int) Option {
	return func(o *Options) {
		o.MaxIdleConns = n
	}
}

// WithConnMaxLifetime 设置连接最大生命周期
func WithConnMaxLifetime(d time.Duration) Option {
	return func(o *Options) {
		o.ConnMaxLifetime = d
	}
}

// WithMaxRetries 设置最大重试次数
func WithMaxRetries(n int) Option {
	return func(o *Options) {
		o.MaxRetries = n
	}
}

// WithRetryDelay 设置重试延迟
func WithRetryDelay(n int) Option {
	return func(o *Options) {
		o.RetryDelay = n
	}
}

// WithLogger 设置GORM日志记录器
func WithLogger(logger logger.Interface) Option {
	return func(o *Options) {
		o.Logger = logger
	}
}

// WithDBName 设置数据库连接名称
func WithDBName(dbName string) Option {
	return func(o *Options) {
		o.DBName = dbName
	}
}

// WithDBType 设置数据库类型
func WithDBType(dbType string) Option {
	return func(o *Options) {
		o.DBType = dbType
	}
}

// WithDSN 设置数据库连接字符串
func WithDSN(dsn string) Option {
	return func(o *Options) {
		o.DSN = dsn
	}
}

// DefaultOptions 返回默认配置
func DefaultOptions() *Options {
	return &Options{
		DBName:          "default",
		DBType:          "sqlite",
		DSN:             "./data/db.sqlite",
		MaxOpenConns:    25,
		MaxIdleConns:    25,
		ConnMaxLifetime: 5 * time.Minute,
		MaxRetries:      3,
		RetryDelay:      1,
		Logger:          nil, // 默认使用nil，表示使用GORM默认日志
	}
}

// InitDB 初始化单个数据库连接
func InitDB(opts ...Option) error {
	options := DefaultOptions()

	// 应用自定义配置
	for _, opt := range opts {
		opt(options)
	}

	// 检查必要参数
	if options.DBName == "" {
		return fmt.Errorf("database name cannot be empty")
	}
	if options.DSN == "" {
		return fmt.Errorf("database DSN cannot be empty")
	}

	// 检查连接是否已存在
	if _, ok := dbConnections[options.DBName]; ok {
		return fmt.Errorf("database connection '%s' already exists", options.DBName)
	}

	// 如果日志记录器为nil，则使用默认日志记录器
	if options.Logger == nil {
		options.Logger = NewDbLogger(
			WithLogFilePath("./logs/db/db.log"),
			WithLogLevel(logger.Info),
			WithLogFormatter(DefaultLogFormatter),
		)
	}

	// 创建数据库连接
	var db *gorm.DB
	var err error

	// 重试连接逻辑
	for i := 0; i < options.MaxRetries; i++ {
		switch options.DBType {
		case "postgres":
			db, err = gorm.Open(postgres.Open(options.DSN), &gorm.Config{Logger: options.Logger})
		case "mysql":
			db, err = gorm.Open(mysql.Open(options.DSN), &gorm.Config{Logger: options.Logger})
		case "sqlite":
			db, err = gorm.Open(sqlite.Open(options.DSN), &gorm.Config{Logger: options.Logger})
		default:
			return fmt.Errorf("unsupported database type: %s", options.DBType)
		}

		if err == nil {
			break
		}

		// 重试连接
		log.Printf("Failed to connect to database: %v. Retrying in %d seconds...", err, options.RetryDelay)
		time.Sleep(time.Duration(options.RetryDelay) * time.Second)
	}

	if err != nil {
		return fmt.Errorf("failed to connect to database after %d attempts: %v", options.MaxRetries, err)
	}

	// 设置连接池
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("failed to get database from GORM: %v", err)
	}

	sqlDB.SetMaxOpenConns(options.MaxOpenConns)
	sqlDB.SetMaxIdleConns(options.MaxIdleConns)
	sqlDB.SetConnMaxLifetime(options.ConnMaxLifetime)

	// 存储连接
	dbConnections[options.DBName] = db
	log.Printf("Database connection '%s' established", options.DBName)
	return nil
}

// GetDB 获取数据库连接
func GetDB(dbName string) (*gorm.DB, error) {
	db, exists := dbConnections[dbName]
	if !exists {
		return nil, fmt.Errorf("database connection '%s' not found", dbName)
	}
	return db, nil
}

// DbList 获取所有数据库连接
func DbList() map[string]*gorm.DB {
	return dbConnections
}

// Create inserts a new record into the specified database
func Create(dbName string, value interface{}) error {
	db, err := GetDB(dbName)
	if err != nil {
		return err
	}
	return db.Create(value).Error
}

// Update modifies an existing record in the specified database
func Update(dbName string, value interface{}) error {
	db, err := GetDB(dbName)
	if err != nil {
		return err
	}
	return db.Save(value).Error
}

// Delete removes a record from the specified database
func Delete(dbName string, value interface{}) error {
	db, err := GetDB(dbName)
	if err != nil {
		return err
	}
	return db.Delete(value).Error
}

// Find retrieves records from the specified database based on conditions
func Find(dbName string, out interface{}, where ...interface{}) error {
	db, err := GetDB(dbName)
	if err != nil {
		return err
	}
	return db.Find(out, where...).Error
}

// ExecSQL executes a raw SQL query on the specified database
func ExecSQL(dbName, sql string, values ...interface{}) error {
	db, err := GetDB(dbName)
	if err != nil {
		return err
	}
	return db.Exec(sql, values...).Error
}

// QuerySQL executes a raw SQL query on the specified database and scans the result into the provided destination
func QuerySQL(dbName string, dest interface{}, sql string, values ...interface{}) error {
	db, err := GetDB(dbName)
	if err != nil {
		return err
	}
	return db.Raw(sql, values...).Scan(dest).Error
}

// ExecuteInTransaction executes the given function within a database transaction
func ExecuteInTransaction(dbName string, txFunc func(tx *gorm.DB) error) error {
	db, err := GetDB(dbName)
	if err != nil {
		return err
	}

	tx := db.Begin()
	if tx.Error != nil {
		return tx.Error
	}

	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
			panic(r) // re-throw the panic after Rollback
		}
	}()

	if err := txFunc(tx); err != nil {
		tx.Rollback()
		return err
	}

	return tx.Commit().Error
}

// PaginateQuery performs a paginated query on the specified table
func PaginateQuery(dbName, tableName string, pageSize int, processFunc func(records []map[string]interface{}) error, conds ...interface{}) error {
	db, err := GetDB(dbName)
	if err != nil {
		return err
	}
	var (
		page       = 1
		totalCount int64
	)

	query := db.Table(tableName)

	// 如果有条件，添加到查询中
	if len(conds) > 0 {
		query = query.Where(conds[0], conds[1:]...)
	}

	// 获取总记录数
	if err := query.Count(&totalCount).Error; err != nil {
		return err
	}

	log.Printf("Total records in %s: %d", tableName, totalCount)

	// 循环分页查询
	for {
		var records []map[string]interface{}

		// 查询当前页的数据
		if err := query.Limit(pageSize).
			Offset((page - 1) * pageSize).
			Find(&records).Error; err != nil {
			return err
		}

		// 如果没有更多数据，退出循环
		if len(records) == 0 {
			break
		}

		// 处理查询到的数据
		if err := processFunc(records); err != nil {
			return err
		}

		log.Printf("Processed page %d with %d records", page, len(records))

		// 如果已经查询完所有数据，退出循环
		if page*pageSize >= int(totalCount) {
			break
		}

		page++
	}

	return nil
}

// Paginate retrieves records from the specified database based on conditions and supports pagination
func Paginate(dbName string, out interface{}, page, pageSize int, where ...interface{}) error {
	db, err := GetDB(dbName)
	if err != nil {
		return err
	}
	offset := (page - 1) * pageSize
	return db.Limit(pageSize).Offset(offset).Find(out, where...).Error
}

// CloseDB closes all database connections
func CloseDB() {
	for dbName, db := range dbConnections {
		sqlDB, err := db.DB()
		if err != nil {
			log.Printf("Failed to get database from GORM: %v", err)
			continue
		}
		sqlDB.Close()
		delete(dbConnections, dbName)
		log.Printf("Database connection '%s' closed successfully", dbName)
	}
}
