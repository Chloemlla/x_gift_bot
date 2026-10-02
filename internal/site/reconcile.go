package site

import (
	"context"
	"fmt"
	"os"
	"syscall"
	"time"
	"xgift/internal/checkout"
)

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
	if err != nil || record == nil || record.Status != "succeeded" || record.RecipientID != c.RecipientID || record.Username != c.Username || record.Months != c.Months || record.Amount != c.Months*10000 || record.Currency != "BDT" {
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
