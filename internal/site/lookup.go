package site

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
)

// lookup finds one redemption code by its full value so the admin can answer
// "where is this code and what happened to it" without paging through batches.
func (s *server) lookup(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Code string `json:"code"`
	}
	if !decode(w, r, &q) {
		return
	}
	q.Code = strings.ToUpper(strings.TrimSpace(q.Code))
	if !codePattern.MatchString(q.Code) {
		message(w, 400, "请填写 XG- 开头、后接 48 位十六进制字符（0-9、A-F）的完整兑换码。")
		return
	}
	var c codeRow
	e := s.db.QueryRow("SELECT id,hint,batch,months,status,username,message,created,updated,progress,COALESCE(folder_id,''),copyable FROM codes WHERE hash=?", hash(q.Code)).Scan(&c.ID, &c.Hint, &c.Batch, &c.Months, &c.Status, &c.Username, &c.Message, &c.Created, &c.Updated, &c.Progress, &c.Folder, &c.Copyable)
	if errors.Is(e, sql.ErrNoRows) {
		message(w, 404, "没有找到这个兑换码，请核对后重试。")
		return
	}
	if e != nil {
		message(w, 503, "暂时无法查询兑换码，请稍后重试。")
		return
	}
	reply(w, 200, c)
}
