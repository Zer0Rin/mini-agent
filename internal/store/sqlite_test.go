package store_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite" // 纯 Go 的 SQLite 驱动，注册名 "sqlite"

	"github.com/Zer0Rin/mini-agent/internal/store"
	"github.com/Zer0Rin/mini-agent/internal/store/storetest"
)

func TestSQLiteContract(t *testing.T) {
	storetest.Run(t, func(t *testing.T) store.Store {
		db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "test.db"))
		if err != nil {
			t.Fatal(err)
		}
		s, err := store.NewSQLite(context.Background(), db)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		return s
	})
}

// 迁移必须幂等：同一个数据库打开两次不能报错
func TestSQLiteMigrateTwice(t *testing.T) {
	path := filepath.Join(t.TempDir(), "twice.db")
	for i := 0; i < 2; i++ {
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		s, err := store.NewSQLite(context.Background(), db)
		if err != nil {
			t.Fatalf("第 %d 次打开失败: %v", i+1, err)
		}
		s.Close()
	}
}
