package site

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
)

func (s *server) list(w http.ResponseWriter, r *http.Request) {
	page := 0
	if raw := r.URL.Query().Get("page"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 || n > 100000 {
			message(w, 400, "页码不正确。")
			return
		}
		page = n
	}
	filter := r.URL.Query().Get("folder")
	if filter != "" && filter != "unfiled" && !folderIDPattern.MatchString(filter) {
		message(w, 400, "文件夹无效。")
		return
	}
	tx, err := s.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		message(w, 503, "无法读取兑换码。")
		return
	}
	defer tx.Rollback()
	where := ""
	args := []any{}
	if filter == "unfiled" {
		where = " WHERE folder_id IS NULL"
	} else if filter != "" {
		var id string
		err = tx.QueryRow("SELECT id FROM folders WHERE id=?", filter).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			message(w, 404, "文件夹不存在，请查看全部兑换码并刷新。")
			return
		}
		if err != nil {
			message(w, 503, "无法读取文件夹。")
			return
		}
		where = " WHERE folder_id=?"
		args = append(args, filter)
	}
	args = append(args, page*100)
	rows, err := tx.Query("SELECT id,hint,batch,months,status,username,message,created,updated,progress,COALESCE(folder_id,''),copyable FROM codes"+where+" ORDER BY created DESC,rowid DESC LIMIT 101 OFFSET ?", args...)
	if err != nil {
		message(w, 503, "无法读取兑换码。")
		return
	}
	codes := []codeRow{}
	for rows.Next() {
		var c codeRow
		if err = rows.Scan(&c.ID, &c.Hint, &c.Batch, &c.Months, &c.Status, &c.Username, &c.Message, &c.Created, &c.Updated, &c.Progress, &c.Folder, &c.Copyable); err != nil {
			rows.Close()
			message(w, 503, "无法读取兑换码。")
			return
		}
		codes = append(codes, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		message(w, 503, "无法读取兑换码。")
		return
	}
	hasMore := len(codes) > 100
	if hasMore {
		codes = codes[:100]
	}
	folders := []folderRow{}
	rows, err = tx.Query("SELECT f.id,f.name,COUNT(c.id) FROM folders f LEFT JOIN codes c ON c.folder_id=f.id GROUP BY f.id ORDER BY f.name COLLATE NOCASE,f.id")
	if err != nil {
		message(w, 503, "无法读取文件夹。")
		return
	}
	for rows.Next() {
		var f folderRow
		if err = rows.Scan(&f.ID, &f.Name, &f.Count); err != nil {
			rows.Close()
			message(w, 503, "无法读取文件夹。")
			return
		}
		folders = append(folders, f)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		message(w, 503, "无法读取文件夹。")
		return
	}
	stats := map[string]int{"total": 0, "active": 0, "processing": 0, "review": 0, "succeeded": 0, "revoked": 0, "unfiled": 0}
	rows, err = tx.Query("SELECT status,COUNT(*) FROM codes GROUP BY status")
	if err != nil {
		message(w, 503, "无法读取统计。")
		return
	}
	for rows.Next() {
		var state string
		var count int
		if err = rows.Scan(&state, &count); err != nil {
			rows.Close()
			message(w, 503, "无法读取统计。")
			return
		}
		stats[state] = count
		stats["total"] += count
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		message(w, 503, "无法读取统计。")
		return
	}
	var unfiled int
	if err = tx.QueryRow("SELECT COUNT(*) FROM codes WHERE folder_id IS NULL").Scan(&unfiled); err != nil {
		message(w, 503, "无法读取统计。")
		return
	}
	stats["unfiled"] = unfiled
	if err = tx.Commit(); err != nil {
		message(w, 503, "无法读取一致的列表，请刷新重试。")
		return
	}
	paymentsReady, _ := s.paymentsAvailable()
	reply(w, 200, map[string]any{"codes": codes, "payments_enabled": paymentsReady, "page": page, "has_more": hasMore, "folder": filter, "folders": folders, "stats": stats})
}
