package site

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"xgift/internal/checkout"
)

// Customer lookup is independent of folder filters and pagination.
func (s *server) customerOrder(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	user, validUser := checkout.NormalizeUsername(r.URL.Query().Get("username"))
	var c codeRow
	var digest string
	query := "SELECT id,hint,batch,months,status,username,message,created,updated,progress,COALESCE(recipient_id,''),copyable,hash FROM codes WHERE "
	var arg string
	if id != "" {
		if !folderIDPattern.MatchString(id) {
			message(w, 400, "订单编号无效。")
			return
		}
		query += "id=?"
		arg = id
	} else {
		if !validUser {
			message(w, 400, "请输入正确的客户 X 用户名。")
			return
		}
		query += "username=?"
		arg = user
	}
	err := s.db.QueryRow(query, arg).Scan(&c.ID, &c.Hint, &c.Batch, &c.Months, &c.Status, &c.Username, &c.Message, &c.Created, &c.Updated, &c.Progress, &c.RecipientID, &c.Copyable, &digest)
	if errors.Is(err, sql.ErrNoRows) {
		message(w, 404, "未找到这个客户的订单。")
		return
	}
	if err != nil {
		message(w, 503, "无法读取客户订单。")
		return
	}
	plain := ""
	if c.Copyable {
		b, e := s.vault.Get("redemption:" + c.ID)
		if e != nil {
			message(w, 503, "无法读取卡密，请稍后重试。")
			return
		}
		defer clear(b)
		if !codePattern.Match(b) || hash(string(b)) != digest {
			message(w, 503, "卡密校验未通过，请核实加密记录。")
			return
		}
		plain = string(b)
	}
	var order checkout.Record
	link := ""
	previousLink := ""
	if c.RecipientID != "" {
		b, e := s.vault.Get("checkout:" + c.RecipientID)
		if e == nil {
			defer clear(b)
			if json.Unmarshal(b, &order) == nil && order.RecipientID == c.RecipientID && order.Username == c.Username && order.Months == c.Months {
				link = checkout.CheckoutLink(&order)
				if order.PreviousSession != "" {
					old, e := s.vault.Get("replacement-original:" + order.PreviousSession)
					if e == nil {
						var audit struct {
							Original checkout.Record `json:"original"`
						}
						if json.Unmarshal(old, &audit) == nil && audit.Original.RecipientID == c.RecipientID && audit.Original.Username == c.Username {
							previousLink = checkout.CheckoutLink(&audit.Original)
						}
						clear(old)
					}
				}
			}
		}
	}
	reply(w, 200, map[string]any{"order": c, "code": plain, "checkout_url": link, "previous_checkout_url": previousLink, "can_recover": c.Status == "review" && c.RecipientID != "", "replacement_count": order.ReplacementCount})
}
