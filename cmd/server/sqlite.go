package main

// 纯 Go 的 SQLite 驱动（不需要 CGO），注册名为 "sqlite"。
// 驱动只在入口处导入，store 包本身只依赖 database/sql。
import _ "modernc.org/sqlite"

const sqliteDriver = "sqlite"
