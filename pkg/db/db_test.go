package db

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 验证使用 SQLite 驱动，仅覆盖与驱动无关的封装语义，不代表 MySQL/Postgres/ClickHouse 已验证。

type account struct {
	ID      uint
	Name    string
	Balance int
	Active  bool
}

func initSQLite(t *testing.T, name string) {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), name+".sqlite")
	if err := InitDB(WithDBName(name), WithDBType("sqlite"), WithDSN(dsn), WithLogger(logger.Discard), WithMaxOpenConns(1), WithMaxIdleConns(1)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(CloseDB)
	d, err := GetDB(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.AutoMigrate(&account{}); err != nil {
		t.Fatal(err)
	}
}

func TestInitDBValidatesParameters(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "x.sqlite")
	cases := map[string][]Option{
		"zero attempts":     {WithDBName("a1"), WithDSN(dsn), WithMaxRetries(0), WithLogger(logger.Discard)},
		"negative attempts": {WithDBName("a2"), WithDSN(dsn), WithMaxRetries(-1), WithLogger(logger.Discard)},
		"negative delay":    {WithDBName("a3"), WithDSN(dsn), WithRetryDelay(-1), WithLogger(logger.Discard)},
		"empty name":        {WithDBName(""), WithDSN(dsn), WithLogger(logger.Discard)},
		"empty dsn":         {WithDBName("a4"), WithDSN(""), WithLogger(logger.Discard)},
		"unknown type":      {WithDBName("a5"), WithDSN(dsn), WithDBType("oracle"), WithLogger(logger.Discard)},
	}
	for name, opts := range cases {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("%s: panicked: %v", name, r)
				}
			}()
			if err := InitDB(opts...); err == nil {
				t.Errorf("%s: expected error", name)
			}
		}()
	}
	if len(DbList()) != 0 {
		t.Errorf("failed InitDB must not register connections: %v", DbList())
	}
}

func TestGetDBNotFoundIsMatchable(t *testing.T) {
	_, err := GetDB("nope")
	if !errors.Is(err, ErrConnectionNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestDbListReturnsSnapshot(t *testing.T) {
	initSQLite(t, "snap")
	list := DbList()
	if len(list) != 1 {
		t.Fatalf("list %v", list)
	}
	delete(list, "snap")
	list["bogus"] = nil
	if _, err := GetDB("snap"); err != nil {
		t.Fatalf("mutating snapshot altered registry: %v", err)
	}
	if _, err := GetDB("bogus"); err == nil {
		t.Fatal("snapshot write leaked into registry")
	}
}

func TestDuplicateInitRejected(t *testing.T) {
	initSQLite(t, "dup")
	err := InitDB(WithDBName("dup"), WithDBType("sqlite"), WithDSN(filepath.Join(t.TempDir(), "y.sqlite")), WithLogger(logger.Discard))
	if err == nil {
		t.Fatal("duplicate name must be rejected")
	}
}

// 供 go test -race 检出未同步的注册表访问。
func TestConcurrentInitGetListClose(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(3)
		go func(i int) {
			defer wg.Done()
			_ = InitDB(WithDBName(fmt.Sprintf("c%d", i%3)), WithDBType("sqlite"), WithDSN(filepath.Join(dir, fmt.Sprintf("c%d.sqlite", i))), WithLogger(logger.Discard))
		}(i)
		go func() { defer wg.Done(); _, _ = GetDB("c1"); _ = DbList() }()
		go func() { defer wg.Done(); CloseDB() }()
	}
	wg.Wait()
	CloseDB()
	if n := len(DbList()); n != 0 {
		t.Fatalf("connections left after CloseDB: %d", n)
	}
}

func TestExecuteInTransactionRollbackAndCommit(t *testing.T) {
	initSQLite(t, "tx")
	boom := errors.New("boom")
	err := ExecuteInTransaction("tx", func(tx *gorm.DB) error {
		if err := tx.Create(&account{Name: "rolled-back", Balance: 1}).Error; err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
	var n int64
	d, _ := GetDB("tx")
	d.Model(&account{}).Count(&n)
	if n != 0 {
		t.Fatalf("rollback left %d rows", n)
	}

	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Error("panic must be re-thrown after rollback")
			}
		}()
		_ = ExecuteInTransaction("tx", func(tx *gorm.DB) error {
			tx.Create(&account{Name: "panic-row"})
			panic("kaboom")
		})
	}()
	d.Model(&account{}).Count(&n)
	if n != 0 {
		t.Fatalf("panic rollback left %d rows", n)
	}

	if err := ExecuteInTransaction("tx", func(tx *gorm.DB) error {
		return tx.Create(&account{Name: "committed"}).Error
	}); err != nil {
		t.Fatal(err)
	}
	d.Model(&account{}).Count(&n)
	if n != 1 {
		t.Fatalf("commit rows = %d", n)
	}
}

// 记录（不改变）当前零值语义：Update=Save 写全部字段，repository 的 Update=Updates 跳过零值。
func TestUpdateUsesSaveSemanticsIncludingZeroValues(t *testing.T) {
	initSQLite(t, "zero")
	a := &account{Name: "n", Balance: 10, Active: true}
	if err := Create("zero", a); err != nil {
		t.Fatal(err)
	}
	a.Balance, a.Active = 0, false
	if err := Update("zero", a); err != nil {
		t.Fatal(err)
	}
	var got account
	d, _ := GetDB("zero")
	d.First(&got, a.ID)
	if got.Balance != 0 || got.Active {
		t.Fatalf("Save-style Update must persist zero values: %+v", got)
	}
}

func TestPaginateValidatesArguments(t *testing.T) {
	initSQLite(t, "pg")
	var out []account
	if err := Paginate("pg", &out, 0, 10); err == nil {
		t.Error("page 0 must be rejected")
	}
	if err := Paginate("pg", &out, 1, 0); err == nil {
		t.Error("pageSize 0 must be rejected")
	}
	noop := func([]map[string]interface{}) error { return nil }
	if err := PaginateQuery("pg", "accounts", 0, noop); err == nil {
		t.Error("pageSize 0 must be rejected (would loop forever)")
	}
	for _, tbl := range []string{"accounts; DROP TABLE accounts", "a b", "", "a.b.c", "x--"} {
		if err := PaginateQuery("pg", tbl, 10, noop); err == nil {
			t.Errorf("table %q must be rejected", tbl)
		}
	}
	d, _ := GetDB("pg")
	for i := 0; i < 25; i++ {
		d.Create(&account{Name: fmt.Sprintf("n%d", i)})
	}
	seen := 0
	if err := PaginateQuery("pg", "accounts", 10, func(r []map[string]interface{}) error { seen += len(r); return nil }); err != nil {
		t.Fatal(err)
	}
	if seen != 25 {
		t.Fatalf("seen %d", seen)
	}
}
