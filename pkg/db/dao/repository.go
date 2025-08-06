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

package dao

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
)

/*
// BaseRepository = 一个通用的"数据库操作工具"
// 泛型参数T = 告诉这个工具要操作哪个表
// Repository[T] = 这个工具的具体类型
*/

// Entity 基础实体约束接口
// 所有要使用Repository的实体都必须实现此接口
type Entity interface {
	TableName() string
	DB() *gorm.DB // 添加返回db 实例方法
}

// Repository 通用Repository接口，提供基础的CRUD操作
type Repository[T Entity] interface {
	// 基础CRUD操作
	// Create: 创建单条记录
	// 参数: ctx - 上下文, entity - 要创建的实体指针
	// 示例: repo.Create(ctx, &user)
	Create(ctx context.Context, entity *T) error

	// Save: 保存记录（创建或更新）
	// 参数: ctx - 上下文, entity - 要保存的实体指针
	// 示例: repo.Save(ctx, &user)
	Save(ctx context.Context, entity *T) error

	// Update: 更新记录
	// 参数: ctx - 上下文, entity - 要更新的实体指针
	// 示例: repo.Update(ctx, &user)
	Update(ctx context.Context, entity *T) error

	// Delete: 删除记录
	// 参数: ctx - 上下文, entity - 要删除的实体指针
	// 示例: repo.Delete(ctx, &user)
	Delete(ctx context.Context, entity *T) error

	// DeleteByID: 根据ID删除记录
	// 参数: ctx - 上下文, id - 要删除的记录ID
	// 示例: repo.DeleteByID(ctx, 1)
	DeleteByID(ctx context.Context, id uint) error

	// 查询操作
	// FindByID: 根据ID查找单条记录
	// 参数: ctx - 上下文, id - 要查找的记录ID
	// 示例: user, err := repo.FindByID(ctx, 1)
	FindByID(ctx context.Context, id uint) (*T, error)

	// FindAll: 查找所有记录
	// 参数: ctx - 上下文
	// 示例: users, err := repo.FindAll(ctx)
	FindAll(ctx context.Context) ([]T, error)

	// FindByCondition: 根据条件查找多条记录
	// 参数: ctx - 上下文, condition - 查询条件, args - 条件参数
	// 示例: users, err := repo.FindByCondition(ctx, "status = ? AND age > ?", "active", 18)
	FindByCondition(ctx context.Context, condition interface{}, args ...interface{}) ([]T, error)

	// FindOneByCondition: 根据条件查找单条记录
	// 参数: ctx - 上下文, condition - 查询条件, args - 条件参数
	// 示例: user, err := repo.FindOneByCondition(ctx, "email = ?", "user@example.com")
	FindOneByCondition(ctx context.Context, condition interface{}, args ...interface{}) (*T, error)

	// 分页查询
	// FindWithPagination: 通用分页查询
	// 参数: ctx - 上下文, page - 页码(从1开始), pageSize - 每页大小, orderBy - 排序字段, desc - 是否降序, condition - 查询条件, args - 条件参数
	// 示例: users, total, err := repo.FindWithPagination(ctx, 1, 10, "created_at", true, "status = ?", "active")
	FindWithPagination(ctx context.Context, page, pageSize int, orderBy string, desc bool, condition interface{}, args ...interface{}) ([]T, int64, error)

	// 统计操作
	// Count: 统计总记录数
	// 参数: ctx - 上下文
	// 示例: count, err := repo.Count(ctx)
	Count(ctx context.Context) (int64, error)

	// CountByCondition: 根据条件统计记录数
	// 参数: ctx - 上下文, condition - 查询条件, args - 条件参数
	// 示例: count, err := repo.CountByCondition(ctx, "status = ?", "active")
	CountByCondition(ctx context.Context, condition interface{}, args ...interface{}) (int64, error)

	// 存在性检查
	// Exists: 检查指定ID的记录是否存在
	// 参数: ctx - 上下文, id - 要检查的记录ID
	// 示例: exists, err := repo.Exists(ctx, 1)
	Exists(ctx context.Context, id uint) (bool, error)

	// ExistsByCondition: 根据条件检查记录是否存在
	// 参数: ctx - 上下文, condition - 查询条件, args - 条件参数
	// 示例: exists, err := repo.ExistsByCondition(ctx, "email = ?", "user@example.com")
	ExistsByCondition(ctx context.Context, condition interface{}, args ...interface{}) (bool, error)

	// 批量操作
	// CreateBatch: 批量创建记录
	// 参数: ctx - 上下文, entities - 要创建的实体切片
	// 示例: err := repo.CreateBatch(ctx, []User{user1, user2, user3})
	CreateBatch(ctx context.Context, entities []T) error

	// UpdateBatch: 批量更新记录
	// 参数: ctx - 上下文, entities - 要更新的实体切片
	// 示例: err := repo.UpdateBatch(ctx, []User{user1, user2, user3})
	UpdateBatch(ctx context.Context, entities []T) error

	// DeleteBatch: 批量删除记录
	// 参数: ctx - 上下文, ids - 要删除的记录ID切片
	// 示例: err := repo.DeleteBatch(ctx, []uint{1, 2, 3})
	DeleteBatch(ctx context.Context, ids []uint) error

	// 事务操作
	// WithTransaction: 执行事务操作
	// 参数: ctx - 上下文, fn - 事务函数
	// 示例: err := repo.WithTransaction(ctx, func(tx *gorm.DB) error { /* 事务逻辑 */ })
	WithTransaction(ctx context.Context, fn func(tx *gorm.DB) error) error

	// 获取原始GORM DB实例，用于复杂查询
	// GetDB: 获取原始GORM DB实例
	// 参数: 无
	// 示例: db := repo.GetDB()
	GetDB() *gorm.DB

	// 原生SQL查询
	// Exec: 执行原生SQL（INSERT、UPDATE、DELETE等）
	// 参数: ctx - 上下文, sql - SQL语句, args - SQL参数
	// 示例: err := repo.Exec(ctx, "UPDATE users SET status = ? WHERE id = ?", "inactive", 1)
	Exec(ctx context.Context, sql string, args ...interface{}) error

	// Query: 执行原生SQL查询，返回gorm.DB实例（用于链式操作）
	// 参数: ctx - 上下文, sql - SQL语句, args - SQL参数
	// 示例: db, err := repo.Query(ctx, "SELECT * FROM users WHERE status = ?", "active")
	Query(ctx context.Context, sql string, args ...interface{}) (*gorm.DB, error)

	// QueryRaw: 执行原生SQL查询，返回原始结果
	// 参数: ctx - 上下文, sql - SQL语句, args - SQL参数
	// 示例: db, err := repo.QueryRaw(ctx, "SELECT * FROM users")
	QueryRaw(ctx context.Context, sql string, args ...interface{}) (*gorm.DB, error)

	// QueryWithPagination: 原生SQL分页查询
	// 参数: ctx - 上下文, sql - SQL语句, page - 页码, pageSize - 每页大小, args - SQL参数
	// 示例: results, total, err := repo.QueryWithPagination(ctx, "SELECT * FROM users WHERE status = ?", 1, 10, "active")
	QueryWithPagination(ctx context.Context, sql string, page, pageSize int, args ...interface{}) ([]map[string]interface{}, int64, error)

	// QueryToStruct: 查询结果映射到结构体
	// 参数: ctx - 上下文, sql - SQL语句, dest - 目标结构体指针, args - SQL参数
	// 示例: err := repo.QueryToStruct(ctx, "SELECT * FROM users", &users)
	QueryToStruct(ctx context.Context, sql string, dest interface{}, args ...interface{}) error

	// QueryToMap: 查询结果映射到map切片
	// 参数: ctx - 上下文, sql - SQL语句, args - SQL参数
	// 示例: results, err := repo.QueryToMap(ctx, "SELECT * FROM users WHERE status = ?", "active")
	QueryToMap(ctx context.Context, sql string, args ...interface{}) ([]map[string]interface{}, error)
}

// BaseRepository 基础Repository实现，可以被具体Repository嵌入
type BaseRepository[T Entity] struct {
	tableName string
	db        *gorm.DB
}

// NewBaseRepository 创建新的Repository实例
func NewBaseRepository[T Entity](db *gorm.DB) Repository[T] {
	var entity T
	return &BaseRepository[T]{
		tableName: entity.TableName(),
		db:        db,
	}
}

// NewBaseRepositoryWithDB 根据Entity的DB方法创建Repository实例
// 参数说明: 无参数，通过泛型T的DB()方法获取数据库实例
// 返回值: (Repository[T], error) - 返回Repository实例和错误信息
func NewBaseRepositoryWithDB[T Entity]() (Repository[T], error) {
	var entity T
	db := entity.DB()
	if db == nil {
		return nil, fmt.Errorf("database instance is nil for entity")
	}
	return &BaseRepository[T]{
		tableName: entity.TableName(),
		db:        db,
	}, nil
}

// GetDB 获取原始GORM DB实例
func (r *BaseRepository[T]) GetDB() *gorm.DB {
	return r.db
}

// GetTableName 获取表名
func (r *BaseRepository[T]) GetTableName() string {
	return r.tableName
}

// Create 创建记录
// 参数说明:
//   - ctx: 上下文，用于超时控制和取消
//   - entity: 要创建的实体指针，不能为nil
//
// 返回值: error - 创建成功返回nil，失败返回错误信息
// 使用示例:
//
//	user := &User{Name: "张三", Email: "zhangsan@example.com"}
//	err := repo.Create(ctx, user)
func (r *BaseRepository[T]) Create(ctx context.Context, entity *T) error {
	if entity == nil {
		return errors.New("entity cannot be nil")
	}
	return r.db.WithContext(ctx).Create(entity).Error
}

// Save 保存记录（创建或更新）
// 参数说明:
//   - ctx: 上下文，用于超时控制和取消
//   - entity: 要保存的实体指针，不能为nil
//
// 返回值: error - 保存成功返回nil，失败返回错误信息
// 使用示例:
//
//	user := &User{ID: 1, Name: "张三", Email: "zhangsan@example.com"}
//	err := repo.Save(ctx, user) // 如果ID存在则更新，不存在则创建
func (r *BaseRepository[T]) Save(ctx context.Context, entity *T) error {
	if entity == nil {
		return errors.New("entity cannot be nil")
	}
	return r.db.WithContext(ctx).Save(entity).Error
}

// Update 更新记录
func (r *BaseRepository[T]) Update(ctx context.Context, entity *T) error {
	if entity == nil {
		return errors.New("entity cannot be nil")
	}
	return r.db.WithContext(ctx).Updates(entity).Error
}

// Delete 删除记录
func (r *BaseRepository[T]) Delete(ctx context.Context, entity *T) error {
	if entity == nil {
		return errors.New("entity cannot be nil")
	}
	return r.db.WithContext(ctx).Delete(entity).Error
}

// DeleteByID 根据ID删除记录
func (r *BaseRepository[T]) DeleteByID(ctx context.Context, id uint) error {
	if id == 0 {
		return errors.New("id cannot be zero")
	}
	var entity T
	return r.db.WithContext(ctx).Delete(&entity, id).Error
}

// FindByID 根据ID查找记录
// 参数说明:
//   - ctx: 上下文，用于超时控制和取消
//   - id: 要查找的记录ID，不能为0
//
// 返回值: (*T, error) - 找到返回实体指针，未找到返回nil和错误信息
// 使用示例:
//
//	user, err := repo.FindByID(ctx, 1)
//	if err != nil {
//	    // 处理错误
//	}
func (r *BaseRepository[T]) FindByID(ctx context.Context, id uint) (*T, error) {
	if id == 0 {
		return nil, errors.New("id cannot be zero")
	}
	var entity T
	err := r.db.WithContext(ctx).First(&entity, id).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("record not found with id: %d", id)
		}
		return nil, err
	}
	return &entity, nil
}

// FindAll 查找所有记录
func (r *BaseRepository[T]) FindAll(ctx context.Context) ([]T, error) {
	var entities []T
	err := r.db.WithContext(ctx).Find(&entities).Error
	return entities, err
}

// FindByCondition 根据条件查找记录
// 参数说明:
//   - ctx: 上下文，用于超时控制和取消
//   - condition: 查询条件，支持GORM的Where条件语法
//   - args: 条件参数，按顺序对应condition中的占位符
//
// 返回值: ([]T, error) - 找到返回实体切片，未找到返回空切片，失败返回错误信息
// 使用示例:
//
//	// 简单条件查询
//	users, err := repo.FindByCondition(ctx, "status = ?", "active")
//
//	// 多条件查询
//	users, err := repo.FindByCondition(ctx, "status = ? AND age > ?", "active", 18)
//
//	// 复杂条件查询
//	users, err := repo.FindByCondition(ctx, "created_at BETWEEN ? AND ?", startTime, endTime)
func (r *BaseRepository[T]) FindByCondition(ctx context.Context, condition interface{}, args ...interface{}) ([]T, error) {
	var entities []T
	err := r.db.WithContext(ctx).Where(condition, args...).Find(&entities).Error
	return entities, err
}

// FindOneByCondition 根据条件查找单条记录
func (r *BaseRepository[T]) FindOneByCondition(ctx context.Context, condition interface{}, args ...interface{}) (*T, error) {
	var entity T
	err := r.db.WithContext(ctx).Where(condition, args...).First(&entity).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("record not found with condition: %v", condition)
		}
		return nil, err
	}
	return &entity, nil
}

// FindWithPagination 通用分页查询
// 参数说明:
//   - ctx: 上下文，用于超时控制和取消
//   - page: 页码，从1开始
//   - pageSize: 每页大小，建议根据业务需求设置合理值
//   - orderBy: 排序字段，为空字符串表示不排序
//   - desc: 是否降序排列，true为降序，false为升序
//   - condition: 查询条件，传nil表示无条件查询
//   - args: 查询条件参数，按顺序对应condition中的占位符
//
// 返回值: ([]T, int64, error) - 返回实体切片、总记录数、错误信息
// 使用示例:
//
//	// 简单分页查询（无条件，无排序）
//	users, total, err := repo.FindWithPagination(ctx, 1, 10, "", false, nil)
//
//	// 带条件的分页查询
//	users, total, err := repo.FindWithPagination(ctx, 1, 10, "", false, "status = ?", "active")
//
//	// 带排序的分页查询
//	users, total, err := repo.FindWithPagination(ctx, 1, 10, "created_at", true, nil)
//
//	// 完整的分页查询（条件+排序）
//	users, total, err := repo.FindWithPagination(ctx, 1, 10, "created_at", true, "status = ? AND age > ?", "active", 18)
func (r *BaseRepository[T]) FindWithPagination(ctx context.Context, page, pageSize int, orderBy string, desc bool, condition interface{}, args ...interface{}) ([]T, int64, error) {

	// 注意：这里不限制pageSize上限，由调用方根据业务需求自行控制
	// 建议在生产环境中根据实际业务场景和系统资源设置合理的上限

	var entities []T
	var total int64

	// 构建查询
	query := r.db.WithContext(ctx).Model(new(T))

	// 添加查询条件（如果提供）
	if condition != nil {
		query = query.Where(condition, args...)
	}

	// 添加排序
	if orderBy != "" {
		if desc {
			query = query.Order(orderBy + " DESC")
		} else {
			query = query.Order(orderBy + " ASC")
		}
	}

	// 获取总数
	err := query.Count(&total).Error
	if err != nil {
		return nil, 0, fmt.Errorf("count query failed: %w", err)
	}

	// 分页查询
	offset := (page - 1) * pageSize
	err = query.Offset(offset).Limit(pageSize).Find(&entities).Error
	if err != nil {
		return nil, 0, fmt.Errorf("find query failed: %w", err)
	}

	return entities, total, nil
}

// Count 统计总数
func (r *BaseRepository[T]) Count(ctx context.Context) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(new(T)).Count(&count).Error
	return count, err
}

// CountByCondition 根据条件统计
func (r *BaseRepository[T]) CountByCondition(ctx context.Context, condition interface{}, args ...interface{}) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(new(T)).Where(condition, args...).Count(&count).Error
	return count, err
}

// Exists 检查记录是否存在
func (r *BaseRepository[T]) Exists(ctx context.Context, id uint) (bool, error) {
	if id == 0 {
		return false, errors.New("id cannot be zero")
	}
	var count int64
	err := r.db.WithContext(ctx).Model(new(T)).Where("id = ?", id).Count(&count).Error
	return count > 0, err
}

// ExistsByCondition 根据条件检查记录是否存在
func (r *BaseRepository[T]) ExistsByCondition(ctx context.Context, condition interface{}, args ...interface{}) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(new(T)).Where(condition, args...).Count(&count).Error
	return count > 0, err
}

// CreateBatch 批量创建
func (r *BaseRepository[T]) CreateBatch(ctx context.Context, entities []T) error {
	if len(entities) == 0 {
		return errors.New("entities slice cannot be empty")
	}
	return r.db.WithContext(ctx).Create(&entities).Error
}

// UpdateBatch 批量更新
func (r *BaseRepository[T]) UpdateBatch(ctx context.Context, entities []T) error {
	if len(entities) == 0 {
		return errors.New("entities slice cannot be empty")
	}
	return r.db.WithContext(ctx).Save(&entities).Error
}

// DeleteBatch 批量删除
func (r *BaseRepository[T]) DeleteBatch(ctx context.Context, ids []uint) error {
	if len(ids) == 0 {
		return errors.New("ids slice cannot be empty")
	}
	return r.db.WithContext(ctx).Delete(new(T), ids).Error
}

// Exec 执行原生SQL（INSERT、UPDATE、DELETE等）
// 参数说明:
//   - ctx: 上下文，用于超时控制和取消
//   - sql: 要执行的SQL语句，支持参数化查询
//   - args: SQL参数，按顺序对应sql中的占位符
//
// 返回值: error - 执行成功返回nil，失败返回错误信息
// 使用示例:
//
//	// 更新操作
//	err := repo.Exec(ctx, "UPDATE users SET status = ? WHERE id = ?", "inactive", 1)
//
//	// 删除操作
//	err := repo.Exec(ctx, "DELETE FROM users WHERE created_at < ?", time.Now().AddDate(0, -1, 0))
//
//	// 插入操作
//	err := repo.Exec(ctx, "INSERT INTO user_logs (user_id, action) VALUES (?, ?)", 1, "login")
func (r *BaseRepository[T]) Exec(ctx context.Context, sql string, args ...interface{}) error {
	result := r.db.WithContext(ctx).Exec(sql, args...)
	if result.Error != nil {
		return fmt.Errorf("exec sql failed: %w", result.Error)
	}
	return nil
}

// Query 执行原生SQL查询，返回gorm.DB实例（用于链式操作）
func (r *BaseRepository[T]) Query(ctx context.Context, sql string, args ...interface{}) (*gorm.DB, error) {
	db := r.db.WithContext(ctx).Raw(sql, args...)
	if db.Error != nil {
		return nil, fmt.Errorf("query sql failed: %w", db.Error)
	}
	return db, nil
}

// QueryRaw 执行原生SQL查询，返回原始结果
func (r *BaseRepository[T]) QueryRaw(ctx context.Context, sql string, args ...interface{}) (*gorm.DB, error) {
	return r.Query(ctx, sql, args...)
}

// QueryWithPagination 原生SQL分页查询
// 参数说明:
//   - ctx: 上下文，用于超时控制和取消
//   - sql: 要执行的SQL查询语句，不支持ORDER BY（会自动添加）
//   - page: 页码，从1开始
//   - pageSize: 每页大小
//   - args: SQL参数，按顺序对应sql中的占位符
//
// 返回值: ([]map[string]interface{}, int64, error) - 返回结果map切片、总记录数、错误信息
// 使用示例:
//
//	// 简单分页查询
//	sql := "SELECT * FROM users WHERE status = ?"
//	results, total, err := repo.QueryWithPagination(ctx, sql, 1, 10, "active")
//
//	// 联表查询分页
//	sql := `
//	    SELECT u.id, u.name, u.email, COUNT(o.id) as order_count
//	    FROM users u
//	    LEFT JOIN orders o ON u.id = o.user_id
//	    WHERE u.status = ?
//	    GROUP BY u.id, u.name, u.email
//	`
//	results, total, err := repo.QueryWithPagination(ctx, sql, 1, 10, "active")
//
//	// 复杂聚合查询分页
//	sql := `
//	    SELECT
//	        DATE(created_at) as date,
//	        COUNT(*) as count,
//	        SUM(amount) as total_amount
//	    FROM orders
//	    WHERE created_at >= ?
//	    GROUP BY DATE(created_at)
//	`
//	results, total, err := repo.QueryWithPagination(ctx, sql, 1, 20, time.Now().AddDate(0, -7, 0))
func (r *BaseRepository[T]) QueryWithPagination(ctx context.Context, sql string, page, pageSize int, args ...interface{}) ([]map[string]interface{}, int64, error) {
	// 构建计数SQL
	countSQL := fmt.Sprintf("SELECT COUNT(*) as total FROM (%s) as t", sql)

	var total int64
	err := r.db.WithContext(ctx).Raw(countSQL, args...).Scan(&total).Error
	if err != nil {
		return nil, 0, fmt.Errorf("count query failed: %w", err)
	}

	// 构建分页SQL
	offset := (page - 1) * pageSize
	paginationSQL := fmt.Sprintf("%s LIMIT %d OFFSET %d", sql, pageSize, offset)

	var results []map[string]interface{}
	err = r.db.WithContext(ctx).Raw(paginationSQL, args...).Scan(&results).Error
	if err != nil {
		return nil, 0, fmt.Errorf("pagination query failed: %w", err)
	}

	return results, total, nil
}

// QueryToStruct 查询结果映射到结构体
func (r *BaseRepository[T]) QueryToStruct(ctx context.Context, sql string, dest interface{}, args ...interface{}) error {
	err := r.db.WithContext(ctx).Raw(sql, args...).Scan(dest).Error
	if err != nil {
		return fmt.Errorf("query to struct failed: %w", err)
	}
	return nil
}

// QueryToMap 查询结果映射到map切片
func (r *BaseRepository[T]) QueryToMap(ctx context.Context, sql string, args ...interface{}) ([]map[string]interface{}, error) {
	var results []map[string]interface{}
	err := r.db.WithContext(ctx).Raw(sql, args...).Scan(&results).Error
	if err != nil {
		return nil, fmt.Errorf("query to map failed: %w", err)
	}
	return results, nil
}

// WithTransaction 事务操作
// 参数说明:
//   - ctx: 上下文，用于超时控制和取消
//   - fn: 事务函数，接收*gorm.DB参数，返回error
//
// 返回值: error - 事务成功返回nil，失败返回错误信息
// 使用示例:
//
//	err := repo.WithTransaction(ctx, func(tx *gorm.DB) error {
//	    // 创建用户
//	    user := &User{Name: "张三", Email: "zhangsan@example.com"}
//	    if err := tx.Create(user).Error; err != nil {
//	        return err
//	    }
//
//	    // 创建用户配置
//	    config := &UserConfig{UserID: user.ID, Theme: "dark"}
//	    if err := tx.Create(config).Error; err != nil {
//	        return err
//	    }
//
//	    return nil
//	})
func (r *BaseRepository[T]) WithTransaction(ctx context.Context, fn func(tx *gorm.DB) error) error {
	return r.db.WithContext(ctx).Transaction(fn)
}
