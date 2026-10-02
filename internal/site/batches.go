package site

import (
	"database/sql"
	"net/http"
	"time"
)

// Batch names are the only classification. Run once so later renames and moves persist.
func migrateBatches(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`CREATE TABLE IF NOT EXISTS site_migrations(name TEXT PRIMARY KEY)`); err != nil {
		return err
	}
	var done int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM site_migrations WHERE name='batch-folders-v2'`).Scan(&done); err != nil {
		return err
	}
	if done == 0 {
		if _, err = tx.Exec(`ALTER TABLE codes ADD COLUMN copyable INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
		if _, err = tx.Exec(`INSERT OR IGNORE INTO folders(id,name,created,updated) SELECT lower(hex(randomblob(16))),batch,MIN(created),MAX(updated) FROM codes WHERE batch<>'' GROUP BY batch COLLATE NOCASE`); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE codes SET folder_id=(SELECT id FROM folders WHERE name=codes.batch COLLATE NOCASE)`); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE codes SET batch=COALESCE((SELECT name FROM folders WHERE id=codes.folder_id),'')`); err != nil {
			return err
		}
		if _, err = tx.Exec(`DELETE FROM folders WHERE id NOT IN (SELECT folder_id FROM codes WHERE folder_id IS NOT NULL)`); err != nil {
			return err
		}
		if _, err = tx.Exec(`INSERT INTO site_migrations(name) VALUES('batch-folders-v2')`); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(`CREATE TRIGGER IF NOT EXISTS batch_rename AFTER UPDATE OF name ON folders BEGIN UPDATE codes SET batch=NEW.name WHERE folder_id=NEW.id; END;
 CREATE TRIGGER IF NOT EXISTS batch_move AFTER UPDATE OF folder_id ON codes BEGIN UPDATE codes SET batch=COALESCE((SELECT name FROM folders WHERE id=NEW.folder_id),'') WHERE id=NEW.id; END;`); err != nil {
		return err
	}
	return tx.Commit()
}

func ensureBatch(tx *sql.Tx, name string) (string, error) {
	id := token(16)
	now := time.Now().Unix()
	if _, err := tx.Exec(`INSERT INTO folders(id,name,created,updated) VALUES(?,?,?,?) ON CONFLICT(name) DO NOTHING`, id, name, now, now); err != nil {
		return "", err
	}
	err := tx.QueryRow(`SELECT id FROM folders WHERE name=? COLLATE NOCASE`, name).Scan(&id)
	return id, err
}

func (s *server) copyCode(w http.ResponseWriter, r *http.Request) {
	var q struct {
		ID string `json:"id"`
	}
	if !decode(w, r, &q) {
		return
	}
	if !folderIDPattern.MatchString(q.ID) {
		message(w, 400, "兑换码无效。")
		return
	}
	var copyable bool
	var digest string
	if err := s.db.QueryRow(`SELECT copyable,hash FROM codes WHERE id=?`, q.ID).Scan(&copyable, &digest); err != nil {
		message(w, 404, "兑换码不存在。")
		return
	}
	if !copyable {
		message(w, 409, "历史兑换码未保存完整内容，无法复制。请使用之前下载的 TXT。")
		return
	}
	plain, err := s.vault.Get("redemption:" + q.ID)
	if err != nil || hash(string(plain)) != digest {
		message(w, 503, "无法读取完整兑换码，请稍后重试。")
		return
	}
	defer clear(plain)
	reply(w, 200, map[string]string{"code": string(plain)})
}
