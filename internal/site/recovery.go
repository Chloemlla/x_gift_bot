package site

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"syscall"
	"time"
	"xgift/internal/checkout"
)

type recoveryItem struct {
	PaymentNode             string `json:"payment_node,omitempty"`
	Hint                    string `json:"hint"`
	CheckoutURL             string `json:"checkout_url,omitempty"`
	NeedsUnpaidVerification bool   `json:"needs_unpaid_verification"`
	ID                      string `json:"id"`
	Username                string `json:"username"`
	Months                  int    `json:"months"`
	Amount                  int    `json:"amount"`
	Currency                string `json:"currency"`
	State                   string `json:"state"`
	Detail                  string `json:"detail"`
	Recipient               string `json:"recipient,omitempty"`
	Digest                  string `json:"digest,omitempty"`
}
type recoveryBatch struct {
	Mode           string         `json:"mode"`
	VerifiedUnpaid bool           `json:"verified_unpaid"`
	ID             string         `json:"id"`
	State          string         `json:"state"`
	Created        int64          `json:"created"`
	Updated        int64          `json:"updated"`
	Last4          string         `json:"last4"`
	Cards          int            `json:"cards"`
	Binding        string         `json:"binding,omitempty"`
	Paused         bool           `json:"paused"`
	Message        string         `json:"message"`
	Items          []recoveryItem `json:"items"`
}

func (s *server) loadRecovery() (*recoveryBatch, error) {
	b, err := s.vault.Get("admin-recovery:latest")
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer clear(b)
	var q recoveryBatch
	if err = json.Unmarshal(b, &q); err != nil {
		return nil, err
	}
	return &q, nil
}
func (s *server) saveRecovery(q *recoveryBatch) error {
	q.Updated = time.Now().Unix()
	b, err := json.Marshal(q)
	if err != nil {
		return err
	}
	defer clear(b)
	if err = s.vault.Put("admin-recovery:"+q.ID, b); err != nil {
		return err
	}
	return s.vault.Put("admin-recovery:latest", b)
}
func recoveryView(q *recoveryBatch) any {
	if q == nil {
		return nil
	}
	out := *q
	out.Binding = ""
	out.Items = append([]recoveryItem{}, q.Items...)
	for i := range out.Items {
		out.Items[i].Recipient = ""
		out.Items[i].Digest = ""
	}
	return out
}
func recoveryActive(q *recoveryBatch) bool {
	return q != nil && (q.State == "running" || q.State == "stopping")
}

// Called once at boot, before accepting requests. No payment task auto-starts.
func (s *server) initRecovery() error {
	q, err := s.loadRecovery()
	if err != nil {
		return err
	}
	if recoveryActive(q) {
		q.State = "interrupted"
		q.Message = "服务重启，任务已暂停。请重新预览；已提交但结果不明的订单不会重付。"
		return s.saveRecovery(q)
	}
	return nil
}
func (s *server) recoveryStatus(w http.ResponseWriter, r *http.Request) {
	s.recoveryMu.Lock()
	defer s.recoveryMu.Unlock()
	q, err := s.loadRecovery()
	if err != nil {
		message(w, 503, "无法读取补单任务。")
		return
	}
	network, err := checkout.PaymentNetworkStatus(s.vault)
	if err != nil {
		message(w, 503, "付款节点池配置无效，请检查配置。")
		return
	}
	cards, err := checkout.CardsStatus(s.vault)
	if err != nil {
		message(w, 503, "无法读取付款卡状态。")
		return
	}
	rotation, err := checkout.PaymentRotationStatus(s.vault)
	if err != nil {
		message(w, 503, "无法读取付款卡轮换状态。")
		return
	}
	paused, err := checkout.PaymentPaused(s.vault)
	if err != nil {
		message(w, 503, "无法读取付款保护状态。")
		return
	}
	// Queue counts describe current orders, independently of the saved last task.
	var review, processing int
	if err = s.db.QueryRow("SELECT COUNT(CASE WHEN status='review' THEN 1 END),COUNT(CASE WHEN status='processing' THEN 1 END) FROM codes").Scan(&review, &processing); err != nil {
		message(w, 503, "无法读取当前订单状态。")
		return
	}
	reply(w, 200, map[string]any{"batch": recoveryView(q), "network": network, "cards": cards, "rotation": rotation, "paused": paused, "summary": map[string]int{"review": review, "processing": processing}})
}
func (s *server) recoveryCandidate(id string) (recoveryItem, error) {
	item := recoveryItem{ID: id, State: "skipped"}
	var status string
	if err := s.db.QueryRow("SELECT username,COALESCE(recipient_id,''),months,status,hint FROM codes WHERE id=?", id).Scan(&item.Username, &item.Recipient, &item.Months, &status, &item.Hint); err != nil {
		return item, err
	}
	plan, err := s.catalogPlan(item.Months)
	if err != nil {
		return item, err
	}
	item.Amount = plan.Minor
	item.Currency = strings.ToUpper(plan.Currency)
	if status != "review" || item.Recipient == "" {
		item.Detail = "订单状态不适合补单"
		return item, nil
	}
	raw, err := s.vault.Get("checkout:" + item.Recipient)
	if errors.Is(err, sql.ErrNoRows) {
		item.Detail = "尚未创建账单，未提交付款；请使用原兑换码和账号重新检查并继续兑换"
		return item, nil
	}
	if err != nil {
		return item, err
	}
	defer clear(raw)
	item.Digest = hash(string(raw))
	var order checkout.Record
	if json.Unmarshal(raw, &order) != nil || order.Username != item.Username || order.RecipientID != item.Recipient || order.Months != item.Months || order.Amount != plan.Minor || order.Currency != item.Currency || order.ProductID != plan.ProductID {
		item.Detail = "原订单身份或金额不一致"
		return item, nil
	}
	item.CheckoutURL = checkout.CheckoutLink(&order)
	item.PaymentNode, err = checkout.PaymentNodeLabel(s.vault, item.Recipient)
	if err != nil {
		return item, err
	}
	item.NeedsUnpaidVerification = checkout.IsPaymentDeclined(&order) || order.Status == "created"
	switch {
	case order.Status == "succeeded":
		item.State = "pending"
		item.Detail = "核对并同步已有成功结果，不付款"
	case order.ManualRecovery && order.RecoveryAttempts >= 3:
		item.Detail = "已达到人工重试上限，请检查付款方式"
	case checkout.ManualRetryBlocked(&order):
		item.Detail = "支付方要求停止或限制重试，请先处理付款方式"
	case checkout.IsPaymentDeclined(&order):
		item.State = "pending"
		item.Detail = "已拒付；核验后处理，失效链接可在确认未扣款后更新"
	case order.Status == "created" || order.Status == "creating":
		item.State = "pending"
		item.Detail = "重新核验未付款订单"
	default:
		item.Detail = "付款结果不明或需要银行验证，禁止再次扣款"
	}
	return item, nil
}
func (s *server) recoveryPreview(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID   string `json:"id"`
		Mode string `json:"mode"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Mode == "" {
		in.Mode = "pay"
	}
	if in.Mode != "pay" && in.Mode != "links" {
		message(w, 400, "补单操作类型无效。")
		return
	}
	if in.ID != "" && !folderIDPattern.MatchString(in.ID) {
		message(w, 400, "订单编号无效。")
		return
	}
	s.recoveryMu.Lock()
	defer s.recoveryMu.Unlock()
	old, err := s.loadRecovery()
	if err != nil {
		message(w, 503, "无法读取补单任务。")
		return
	}
	if recoveryActive(old) {
		message(w, 409, "已有补单任务运行中，请先查看或停止。")
		return
	}
	last4, cardCount, binding, err := checkout.CardSummary(s.vault)
	if err != nil {
		if in.Mode != "links" {
			message(w, 503, "付款方式配置无法读取。")
			return
		}
		// 仅生成链接不提交付款卡;未配置付款卡时也允许预览。
		last4, cardCount, binding = "", 0, ""
	}
	paused, err := checkout.PaymentPaused(s.vault)
	if err != nil {
		message(w, 503, "无法读取付款保护状态。")
		return
	}
	ids := []string{}
	if in.ID != "" {
		ids = append(ids, in.ID)
	} else {
		rows, e := s.db.Query("SELECT id FROM codes WHERE status='review' ORDER BY updated,id")
		if e != nil {
			message(w, 503, "无法读取订单。")
			return
		}
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				break
			}
			ids = append(ids, id)
		}
		if e == nil {
			e = rows.Err()
		}
		rows.Close()
		if e != nil {
			message(w, 503, "无法读取订单。")
			return
		}
	}

	q := &recoveryBatch{Mode: in.Mode, ID: token(16), State: "preview", Created: time.Now().Unix(), Last4: last4, Cards: cardCount, Binding: binding, Paused: paused, Items: []recoveryItem{}}
	for _, id := range ids {
		item, e := s.recoveryCandidate(id)
		if e != nil {
			message(w, 503, "无法核验原订单，请稍后重试。")
			return
		}
		q.Items = append(q.Items, item)
	}
	q.Message = "预览有效期 10 分钟。确认后每笔最多尝试一次，逐笔间隔至少 30 秒。"
	if q.Mode == "links" {
		q.Message = "仅准备付款链接，不付款。预览有效期 10 分钟；请核对客户和旧订单扣款结果。"
	}
	if err = s.saveRecovery(q); err != nil {
		message(w, 503, "无法保存预览。")
		return
	}
	reply(w, 200, map[string]any{"batch": recoveryView(q)})
}
func (s *server) recoveryStart(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID             string `json:"id"`
		Confirm        bool   `json:"confirm"`
		Reset          bool   `json:"reset_pause"`
		VerifiedUnpaid bool   `json:"verified_unpaid"`
	}
	if !decode(w, r, &in) {
		return
	}
	s.recoveryMu.Lock()
	defer s.recoveryMu.Unlock()
	q, err := s.loadRecovery()
	if err != nil {
		message(w, 503, "无法读取补单任务。")
		return
	}
	if q == nil || q.ID != in.ID || !in.Confirm {
		message(w, 409, "请先预览并确认补单清单。")
		return
	}
	if q.State != "preview" {
		reply(w, 200, map[string]any{"batch": recoveryView(q)})
		return
	}
	if time.Now().Unix()-q.Created > 600 {
		message(w, 409, "预览已过期，请重新生成。")
		return
	}
	select {
	case s.work <- struct{}{}:
	default:
		message(w, 409, "当前有订单处理中，请稍后再试。")
		return
	}
	handed := false
	defer func() {
		if !handed {
			<-s.work
		}
	}()
	lock, err := os.OpenFile(s.lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		message(w, 503, "无法锁定付款队列。")
		return
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		message(w, 409, "另一任务正在处理订单。")
		return
	}
	release := func() { syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); lock.Close() }
	defer func() {
		if !handed {
			release()
		}
	}()
	binding := q.Binding
	// 仅生成链接不涉及付款卡,跳过卡集一致性核验。
	if q.Mode != "links" {
		_, _, current, err := checkout.CardSummary(s.vault)
		if err != nil || current != binding {
			message(w, 409, "付款方式已变化，请重新预览。")
			return
		}
	}
	count := 0
	for _, item := range q.Items {
		if item.State != "pending" {
			continue
		}
		current, e := s.recoveryCandidate(item.ID)
		if e != nil || current != item {
			message(w, 409, "订单状态已变化，请重新预览。")
			return
		}
		count++
	}
	if count == 0 {
		message(w, 409, "当前没有能够安全尝试的订单。")
		return
	}
	paused, err := checkout.PaymentPaused(s.vault)
	if err != nil {
		message(w, 503, "无法读取付款保护状态。")
		return
	}
	if q.Mode != "links" && paused && !in.Reset {
		message(w, 409, "付款保护已暂停，请勾选确认重新尝试。")
		return
	}
	q.VerifiedUnpaid = in.VerifiedUnpaid
	q.State = "running"
	q.Message = "管理员已确认，正在逐笔核验和补单。"
	if q.Mode == "links" {
		q.Message = "正在逐笔核验并准备付款链接，本任务不付款。"
	}
	if err = s.saveRecovery(q); err != nil {
		message(w, 503, "无法保存补单任务，未启动。")
		return
	}
	if q.Mode != "links" && paused {
		if err = checkout.ResetManualPaymentPause(s.vault); err != nil {
			q.State = "stopped"
			q.Message = "无法恢复付款保护，任务未启动。"
			_ = s.saveRecovery(q)
			message(w, 503, q.Message)
			return
		}
	}
	handed = true
	s.jobs.Add(1)
	go func() {
		defer s.jobs.Done()
		defer func() { <-s.work }()
		defer release()
		s.runRecovery(q.ID, binding)
	}()
	reply(w, 202, map[string]any{"batch": recoveryView(q)})
}
func (s *server) recoveryStop(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID string `json:"id"`
	}
	if !decode(w, r, &in) {
		return
	}
	s.recoveryMu.Lock()
	defer s.recoveryMu.Unlock()
	q, err := s.loadRecovery()
	if err != nil || q == nil || q.ID != in.ID {
		message(w, 409, "任务已变化，请刷新。")
		return
	}
	if recoveryActive(q) {
		q.State = "stopping"
		q.Message = "正在停止：当前订单完成核实后，不再开始后续订单。"
		if err = s.saveRecovery(q); err != nil {
			message(w, 503, "停止请求未保存，请重试。")
			return
		}
	}
	reply(w, 200, map[string]any{"batch": recoveryView(q)})
}
func (s *server) finishRecovery(id, state, msg string) {
	s.recoveryMu.Lock()
	defer s.recoveryMu.Unlock()
	q, err := s.loadRecovery()
	if err != nil || q == nil || q.ID != id {
		return
	}
	q.State = state
	q.Message = msg
	if s.saveRecovery(q) != nil {
		fmt.Println("admin recovery state could not be saved; worker stopped")
	}
}
func (s *server) runRecovery(id, binding string) {
	first := true
	for {
		if !first {
			select {
			case <-s.ctx.Done():
				s.finishRecovery(id, "interrupted", "服务停止，补单暂停；请核实原订单。")
				return
			case <-time.After(30 * time.Second):
			}
		}
		first = false
		s.recoveryMu.Lock()
		q, err := s.loadRecovery()
		if err != nil || q == nil || q.ID != id {
			s.recoveryMu.Unlock()
			return
		}
		if q.State != "running" || s.ctx.Err() != nil {
			s.recoveryMu.Unlock()
			s.finishRecovery(id, "stopped", "补单已停止，未开始的订单保留待处理。")
			return
		}
		index := -1
		for i := range q.Items {
			if q.Items[i].State == "pending" {
				index = i
				break
			}
		}
		if index < 0 {
			s.recoveryMu.Unlock()
			s.finishRecovery(id, "completed", "本批次处理结束，请查看每笔结果；跳过或失败的订单尚未完成。")
			return
		}
		item := q.Items[index]
		q.Items[index].State = "running"
		q.Items[index].Detail = "正在核验原订单和付款结果"
		err = s.saveRecovery(q)
		s.recoveryMu.Unlock()
		if err != nil {
			return
		}
		state, detail, stop := s.recoverOneOptions(item, binding, q.Mode, q.VerifiedUnpaid)
		s.recoveryMu.Lock()
		q, err = s.loadRecovery()
		if err != nil || q == nil || q.ID != id {
			s.recoveryMu.Unlock()
			return
		}
		q.Items[index].State = state
		q.Items[index].Detail = detail
		if current, e := s.recoveryCandidate(item.ID); e == nil {
			q.Items[index].CheckoutURL = current.CheckoutURL
			q.Items[index].PaymentNode = current.PaymentNode
		}
		hasPending := false
		for _, entry := range q.Items {
			if entry.State == "pending" {
				hasPending = true
				break
			}
		}
		done := !hasPending && !stop && q.State == "running"
		if done {
			q.State = "completed"
			q.Message = "本批次处理结束，请查看每笔结果；跳过或失败的订单尚未完成。"
		}
		if q.State == "stopping" {
			q.State = "stopped"
			q.Message = "已停止后续订单。"
			done = true
		}
		err = s.saveRecovery(q)
		s.recoveryMu.Unlock()
		if err != nil {
			return
		}
		if done {
			return
		}
		if stop {
			s.finishRecovery(id, "stopped", "付款保护已停止本批次。请检查失败原因后重新预览。")
			return
		}
	}
}
func (s *server) recoverOne(item recoveryItem, binding string) (state, detail string, stop bool) {
	return s.recoverOneOptions(item, binding, "pay", false)
}
func (s *server) recoverOneOptions(item recoveryItem, binding, mode string, verified bool) (state, detail string, stop bool) {
	if mode != "links" {
		_, _, current, err := checkout.CardSummary(s.vault)
		if err != nil || current != binding {
			return "blocked", "付款方式配置变化，已停止", true
		}
	}
	checked, err := s.recoveryCandidate(item.ID)
	if err != nil || checked != item {
		return "skipped", "原订单状态已变化，请重新核实", false
	}
	beforeRaw, readErr := s.vault.Get("checkout:" + item.Recipient)
	if readErr != nil {
		return "blocked", "无法读取原订单，已停止", true
	}
	var before checkout.Record
	if json.Unmarshal(beforeRaw, &before) != nil {
		clear(beforeRaw)
		return "blocked", "原订单无法核实，已停止", true
	}
	clear(beforeRaw)
	ctx, cancel := context.WithTimeout(s.ctx, 240*time.Second)
	defer cancel()
	record, err := checkout.RecoverWithNewLink(ctx, s.vault, item.Username, item.Recipient, s.port, item.Months, verified, mode == "links")
	state, detail = "blocked", "未能安全完成，原订单已保留；请检查诊断记录"
	if record != nil && record.Status == "succeeded" && record.Username == item.Username && record.RecipientID == item.Recipient && record.Months == item.Months && record.Amount == item.Amount && record.Currency == item.Currency {
		state = "succeeded"
		detail = fmt.Sprintf("已为 @%s 完成 %d 个月 Premium 赠送。", item.Username, item.Months)
	} else if err == nil && mode == "links" && checkout.CheckoutLink(record) != "" {
		state, detail = "link_ready", "补单付款链接已准备好，本次未付款。"
	} else if checkout.IsPaymentDeclined(record) && (record.RecoveryAttempts > before.RecoveryAttempts || record.SubmittedAt > before.SubmittedAt) {
		state = "declined"
		detail = "付款被拒绝，请检查银行卡或联系发卡行。"
		if record.LastError != nil {
			detail += fmt.Sprintf(" 错误：%s；拒付原因：%s；建议：%s；请求编号：%s", record.LastError.Code, record.LastError.DeclineCode, record.LastError.AdviceCode, record.LastError.RequestID)
		}
	} else if record != nil && record.Status == "requires_action" {
		state = "requires_action"
		detail = "需要持卡人完成银行验证，未再次提交。"
		stop = true
	}
	if errors.Is(err, checkout.ErrNotEligible) || errors.Is(err, checkout.ErrUserNotFound) {
		state = "skipped"
		detail = "该账号目前无法接收赠送，已跳过。"
	}
	if err != nil {
		b, _ := json.Marshal(map[string]any{"error": err.Error(), "order_id": item.ID, "observed_at": time.Now().Unix()})
		if s.vault.Put("manual-recovery-error:"+item.ID, b) != nil {
			stop = true
		}
		clear(b)
	}
	if state == "blocked" && err != nil && checkout.IsPaymentDeclined(&before) && record != nil && record.RecoveryAttempts == before.RecoveryAttempts && record.SubmittedAt == before.SubmittedAt {
		state = "skipped"
		detail = "原账单未通过重试核验，已跳过，本次未提交补单付款。" + checkout.ManualRecoveryErrorMessage(err)
	}
	if errors.Is(err, checkout.ErrVerifyUnpaid) {
		state = "needs_verification"
		detail = checkout.ManualRecoveryErrorMessage(err)
	}
	if errors.Is(err, checkout.ErrPaymentPaused) {
		stop = true
	}
	if state == "blocked" {
		detail = checkout.ManualRecoveryErrorMessage(err)
		stop = true
	}
	if paused, e := checkout.PaymentPaused(s.vault); e != nil || paused && mode != "links" {
		stop = true
	}
	siteState := "review"
	if state == "succeeded" {
		siteState = "succeeded"
	}
	res, e := s.db.Exec("UPDATE codes SET status=?,message=?,updated=?,progress=CASE WHEN ?='succeeded' THEN 100 ELSE progress END WHERE id=? AND status='review' AND username=? AND recipient_id=? AND months=?", siteState, detail, time.Now().Unix(), siteState, item.ID, item.Username, item.Recipient, item.Months)
	if e != nil {
		return "blocked", "结果写入失败，请核实原订单；任务已停止", true
	}
	if n, e := res.RowsAffected(); e != nil || n != 1 {
		return "blocked", "订单状态发生变化，请核实结果", true
	}
	return
}
