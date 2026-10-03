package site

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"
	"xgift/internal/checkout"
)

// catalogPlan resolves the configured plan per flow, never at startup, so the
// site boots before the operator writes the catalog record.
func (s *server) catalogPlan(months int) (checkout.Plan, error) {
	catalog, err := checkout.ReadCatalog(s.vault)
	if err != nil {
		return checkout.Plan{}, err
	}
	return catalog.PlanFor(months)
}

// A status query can repair delayed/lost success writes, but can never pay.
func (s *server) reconcileStatus(ctx context.Context, c *codeRow) {
	if c.Status != "review" || c.RecipientID == "" {
		return
	}
	select {
	case s.work <- struct{}{}:
	default:
		return
	}
	defer func() { <-s.work }()
	lock, err := os.OpenFile(s.lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return
	}
	defer lock.Close()
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	ctx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	record, err := checkout.Reconcile(ctx, s.vault, c.RecipientID, s.port)
	if err != nil || record == nil || record.Status != "succeeded" || record.RecipientID != c.RecipientID || record.Username != c.Username || record.Months != c.Months {
		return
	}
	plan, err := s.catalogPlan(c.Months)
	if err != nil || record.Amount != plan.Minor || record.Currency != strings.ToUpper(plan.Currency) {
		return
	}
	msg := fmt.Sprintf("已为 @%s 完成 %d 个月 Premium 赠送。打开 X 查看会员状态；如未刷新，请重新打开 X。", c.Username, c.Months)
	result, err := s.db.Exec("UPDATE codes SET status='succeeded',progress=100,message=?,updated=? WHERE id=? AND recipient_id=? AND username=? AND months=? AND status='review'", msg, time.Now().Unix(), c.ID, c.RecipientID, c.Username, c.Months)
	if err != nil {
		return
	}
	n, err := result.RowsAffected()
	if err == nil && n == 1 {
		c.Status = "succeeded"
		c.Progress = 100
		c.Message = msg
	}
}

// Background reconciliation never creates an order or submits payment. It checks
// one pending record per tick with a short deadline, yielding to live checkouts.
func (s *server) reconcileLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	cursor := ""
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
		}
		var c codeRow
		err := s.db.QueryRow("SELECT id,recipient_id,username,months,status,progress,updated FROM codes WHERE status='review' AND recipient_id IS NOT NULL AND id>? AND updated>? ORDER BY id LIMIT 1", cursor, time.Now().Add(-24*time.Hour).Unix()).Scan(&c.ID, &c.RecipientID, &c.Username, &c.Months, &c.Status, &c.Progress, &c.Updated)
		if err != nil {
			cursor = ""
			continue
		}
		cursor = c.ID
		ctx, cancel := context.WithTimeout(s.ctx, 8*time.Second)
		s.reconcileStatus(ctx, &c)
		cancel()
	}
}

func (s *server) autoChecking(c *codeRow) bool {
	if c.Status != "review" || c.RecipientID == "" || c.Updated < time.Now().Add(-24*time.Hour).Unix() {
		return false
	}
	raw, err := s.vault.Get("checkout:" + c.RecipientID)
	if err != nil {
		return false
	}
	defer clear(raw)
	var r checkout.Record
	if json.Unmarshal(raw, &r) != nil {
		return false
	}
	return r.RecipientID == c.RecipientID && r.Username == c.Username && r.Months == c.Months && r.SubmittedAt > 0 && (r.Status == "unknown" || r.Status == "submitting")
}
