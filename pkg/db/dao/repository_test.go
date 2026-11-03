package dao

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 使用 SQLite 验证 Repository 的与驱动无关语义，不代表 MySQL/Postgres 已验证。

type Account struct {
	ID      uint
	Name    string
	Balance int
	Active  bool
}

var testDB *gorm.DB

func (Account) TableName() string { return "accounts" }
func (Account) DB() *gorm.DB      { return testDB }

func newRepo(t *testing.T) Repository[Account] {
	t.Helper()
	d, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "t.sqlite")), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := d.DB()
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { sqlDB.Close() })
	if err := d.AutoMigrate(&Account{}); err != nil {
		t.Fatal(err)
	}
	testDB = d
	return NewBaseRepository[Account](d)
}

func TestNotFoundKeepsErrorIdentity(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	if _, err := repo.FindByID(ctx, 42); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("FindByID: errors.Is(ErrRecordNotFound) failed: %v", err)
	}
	if _, err := repo.FindOneByCondition(ctx, "name = ?", "nobody"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("FindOneByCondition: %v", err)
	}
}

func TestFindWithPaginationOrderingWhitelist(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	for i, n := range []string{"b", "c", "a"} {
		if err := repo.Create(ctx, &Account{Name: n, Balance: i}); err != nil {
			t.Fatal(err)
		}
	}
	got, total, err := repo.FindWithPagination(ctx, 1, 10, "name", false, nil)
	if err != nil || total != 3 || got[0].Name != "a" || got[2].Name != "c" {
		t.Fatalf("asc: %+v %d %v", got, total, err)
	}
	got, _, err = repo.FindWithPagination(ctx, 1, 10, "Balance", true, nil) // Go 字段名
	if err != nil || got[0].Balance != 2 {
		t.Fatalf("desc by field name: %+v %v", got, err)
	}
	got, _, err = repo.FindWithPagination(ctx, 1, 2, "", false, "balance >= ?", 0)
	if err != nil || len(got) != 2 {
		t.Fatalf("no order: %+v %v", got, err)
	}

	for _, bad := range []string{
		"name; DROP TABLE accounts", "name DESC, (SELECT 1)", "1", "unknown_col", "name--", "id) UNION SELECT",
		"accounts.name", "`name`",
	} {
		if _, _, err := repo.FindWithPagination(ctx, 1, 10, bad, false, nil); err == nil {
			t.Errorf("orderBy %q must be rejected", bad)
		}
	}
	// 注入尝试没有副作用
	if n, _ := repo.Count(ctx); n != 3 {
		t.Fatalf("rows after injection attempts: %d", n)
	}
	if _, _, err := repo.FindWithPagination(ctx, 0, 10, "", false, nil); err == nil {
		t.Error("page 0 must be rejected")
	}
	if _, _, err := repo.FindWithPagination(ctx, 1, 0, "", false, nil); err == nil {
		t.Error("pageSize 0 must be rejected")
	}
}

// 文档化当前零值语义：Update(Updates) 忽略零值字段，Save 写入全部字段，UpdateByCondition(map) 可写零值。
func TestZeroValueUpdateSemantics(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	a := &Account{Name: "n", Balance: 10, Active: true}
	if err := repo.Create(ctx, a); err != nil {
		t.Fatal(err)
	}

	zero := &Account{ID: a.ID, Name: "renamed", Balance: 0, Active: false}
	if err := repo.Update(ctx, zero); err != nil {
		t.Fatal(err)
	}
	got, _ := repo.FindByID(ctx, a.ID)
	if got.Name != "renamed" || got.Balance != 10 || !got.Active {
		t.Fatalf("Update must skip zero-value fields: %+v", got)
	}

	if err := repo.Save(ctx, &Account{ID: a.ID, Name: "saved"}); err != nil {
		t.Fatal(err)
	}
	got, _ = repo.FindByID(ctx, a.ID)
	if got.Name != "saved" || got.Balance != 0 || got.Active {
		t.Fatalf("Save must write zero values: %+v", got)
	}

	_ = repo.Save(ctx, &Account{ID: a.ID, Name: "x", Balance: 5, Active: true})
	if err := repo.UpdateByCondition(ctx, map[string]interface{}{"balance": 0, "active": false}, "id = ?", a.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = repo.FindByID(ctx, a.ID)
	if got.Balance != 0 || got.Active {
		t.Fatalf("map update must write zero values: %+v", got)
	}
}

func TestWithTransactionRollsBack(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	boom := fmt.Errorf("boom")
	err := repo.WithTransaction(ctx, func(tx *gorm.DB) error {
		if err := tx.Create(&Account{Name: "x"}).Error; err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if n, _ := repo.Count(ctx); n != 0 {
		t.Fatalf("rows after rollback: %d", n)
	}
}
