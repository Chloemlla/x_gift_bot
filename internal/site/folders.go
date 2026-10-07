package site

import (
	"database/sql"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var folderIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

type folderRow struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// The migration is additive: existing codes remain unclassified.
func migrateFolders(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`CREATE TABLE IF NOT EXISTS folders (
 id TEXT PRIMARY KEY, name TEXT NOT NULL COLLATE NOCASE UNIQUE,
 created INTEGER NOT NULL, updated INTEGER NOT NULL
 )`); err != nil {
		return err
	}
	rows, err := tx.Query("PRAGMA table_info(codes)")
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var cid, required, primary int
		var name, typ string
		var defaultValue any
		if err = rows.Scan(&cid, &name, &typ, &required, &defaultValue, &primary); err != nil {
			rows.Close()
			return err
		}
		if name == "folder_id" {
			found = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if !found {
		if _, err = tx.Exec("ALTER TABLE codes ADD COLUMN folder_id TEXT REFERENCES folders(id) ON DELETE SET NULL"); err != nil {
			return err
		}
	}
	if _, err = tx.Exec("CREATE INDEX IF NOT EXISTS codes_folder_created ON codes(folder_id,created)"); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *server) createFolder(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &q) {
		return
	}
	q.Name = strings.TrimSpace(q.Name)
	if q.Name == "" || len(q.Name) > 120 {
		message(w, 400, "文件夹名称不能为空，且不能超过 120 字节。")
		return
	}
	id, now := token(16), time.Now().Unix()
	result, err := s.db.Exec("INSERT INTO folders(id,name,created,updated) VALUES(?,?,?,?) ON CONFLICT(name) DO NOTHING", id, q.Name, now, now)
	if err != nil {
		message(w, 503, "创建文件夹失败，请刷新后核实。")
		return
	}
	n, err := result.RowsAffected()
	if err != nil {
		message(w, 503, "创建结果不确定，请刷新文件夹列表。")
		return
	}
	if n == 0 {
		message(w, 409, "已存在同名文件夹。")
		return
	}
	reply(w, 201, folderRow{ID: id, Name: q.Name})
}

func (s *server) renameFolder(w http.ResponseWriter, r *http.Request) {
	var q struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if !decode(w, r, &q) {
		return
	}
	q.Name = strings.TrimSpace(q.Name)
	if !folderIDPattern.MatchString(q.ID) || q.Name == "" || len(q.Name) > 120 {
		message(w, 400, "请填写有效的文件夹和名称（最多 120 字节）。")
		return
	}
	result, err := s.db.Exec("UPDATE OR IGNORE folders SET name=?,updated=? WHERE id=?", q.Name, time.Now().Unix(), q.ID)
	if err != nil {
		message(w, 503, "重命名失败，请刷新后核实。")
		return
	}
	n, err := result.RowsAffected()
	if err != nil {
		message(w, 503, "重命名结果不确定，请刷新后核实。")
		return
	}
	if n == 0 {
		message(w, 409, "文件夹不存在或名称已被使用，请刷新后重试。")
		return
	}
	message(w, 200, "文件夹已重命名。")
}

func (s *server) deleteFolder(w http.ResponseWriter, r *http.Request) {
	var q struct {
		ID string `json:"id"`
	}
	if !decode(w, r, &q) {
		return
	}
	if !folderIDPattern.MatchString(q.ID) {
		message(w, 400, "文件夹无效。")
		return
	}
	// ON DELETE SET NULL moves contents back to unclassified; codes are never deleted.
	result, err := s.db.Exec("DELETE FROM folders WHERE id=?", q.ID)
	if err != nil {
		message(w, 503, "删除文件夹失败，请刷新后核实。")
		return
	}
	n, err := result.RowsAffected()
	if err != nil {
		message(w, 503, "删除结果不确定，请刷新后核实。")
		return
	}
	if n == 0 {
		message(w, 404, "文件夹不存在，请刷新列表。")
		return
	}
	message(w, 200, "文件夹已删除，兑换码已移至未分类。")
}

func (s *server) moveCodes(w http.ResponseWriter, r *http.Request) {
	var q struct {
		IDs    []string `json:"ids"`
		Folder string   `json:"folder"`
	}
	if !decode(w, r, &q) {
		return
	}
	if len(q.IDs) < 1 || len(q.IDs) > 1000 || (q.Folder != "" && !folderIDPattern.MatchString(q.Folder)) {
		message(w, 400, "请选择 1–1000 枚兑换码和有效的目标文件夹。")
		return
	}
	seen := map[string]bool{}
	placeholders := make([]string, len(q.IDs))
	args := make([]any, len(q.IDs))
	for i, id := range q.IDs {
		if !folderIDPattern.MatchString(id) || seen[id] {
			message(w, 400, "兑换码选择无效或重复。")
			return
		}
		seen[id] = true
		placeholders[i] = "?"
		args[i] = id
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		message(w, 503, "暂时无法移动兑换码。")
		return
	}
	defer tx.Rollback()
	if q.Folder != "" {
		var id string
		err = tx.QueryRow("SELECT id FROM folders WHERE id=?", q.Folder).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			message(w, 404, "目标文件夹不存在，请刷新列表。")
			return
		}
		if err != nil {
			message(w, 503, "无法读取目标文件夹。")
			return
		}
	}
	where := "id IN (" + strings.Join(placeholders, ",") + ")"
	var count int
	if err = tx.QueryRow("SELECT COUNT(*) FROM codes WHERE "+where, args...).Scan(&count); err != nil {
		message(w, 503, "无法读取选择的兑换码。")
		return
	}
	if count != len(q.IDs) {
		message(w, 409, "部分兑换码已不存在，请刷新列表。")
		return
	}
	var folder any
	if q.Folder != "" {
		folder = q.Folder
	}
	if _, err = tx.Exec("UPDATE codes SET folder_id=? WHERE "+where, append([]any{folder}, args...)...); err != nil {
		message(w, 503, "移动失败，没有修改分类。")
		return
	}
	if err = tx.Commit(); err != nil {
		message(w, 503, "移动结果不确定，请刷新后核实。")
		return
	}
	message(w, 200, "已更新兑换码分类。")
}
