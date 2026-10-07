package site

import (
	"database/sql"
	"math"
	"net/http"
	"time"
)

type statsMonthsRow struct {
	Months    int `json:"months"`
	Total     int `json:"total"`
	Succeeded int `json:"succeeded"`
}

type statsDailyRow struct {
	Date      string `json:"date"`
	Created   int    `json:"created"`
	Redeemed  int    `json:"redeemed"`
	Succeeded int    `json:"succeeded"`
}

type statsStageRow struct {
	Progress int `json:"progress"`
	Count    int `json:"count"`
}

func rate(part, whole int) float64 {
	if whole == 0 {
		return 0
	}
	return math.Round(float64(part)/float64(whole)*10000) / 10000
}

func (s *server) stats(w http.ResponseWriter, r *http.Request) {
	tx, err := s.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		message(w, 503, "无法读取统计。")
		return
	}
	defer tx.Rollback()
	codes := map[string]int{"total": 0, "active": 0, "processing": 0, "review": 0, "succeeded": 0, "revoked": 0, "redeemed": 0, "unfiled": 0}
	rows, err := tx.Query("SELECT status,COUNT(*) FROM codes GROUP BY status")
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
		codes[state] = count
		codes["total"] += count
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		message(w, 503, "无法读取统计。")
		return
	}
	var redeemed, unfiled int
	if err = tx.QueryRow("SELECT COUNT(*) FROM codes WHERE username<>''").Scan(&redeemed); err != nil {
		message(w, 503, "无法读取统计。")
		return
	}
	if err = tx.QueryRow("SELECT COUNT(*) FROM codes WHERE folder_id IS NULL").Scan(&unfiled); err != nil {
		message(w, 503, "无法读取统计。")
		return
	}
	codes["redeemed"] = redeemed
	codes["unfiled"] = unfiled
	months := []statsMonthsRow{}
	rows, err = tx.Query("SELECT months,COUNT(*),SUM(status='succeeded') FROM codes GROUP BY months ORDER BY months")
	if err != nil {
		message(w, 503, "无法读取统计。")
		return
	}
	for rows.Next() {
		var m statsMonthsRow
		if err = rows.Scan(&m.Months, &m.Total, &m.Succeeded); err != nil {
			rows.Close()
			message(w, 503, "无法读取统计。")
			return
		}
		months = append(months, m)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		message(w, 503, "无法读取统计。")
		return
	}
	now := time.Now()
	year, month, day := now.Date()
	today := time.Date(year, month, day, 0, 0, 0, 0, now.Location())
	since := today.AddDate(0, 0, -29).Unix()
	createdByDay := map[string]int{}
	redeemedByDay := map[string]int{}
	succeededByDay := map[string]int{}
	dailyQueries := []struct {
		sql  string
		into map[string]int
	}{
		{"SELECT date(created,'unixepoch','localtime'),COUNT(*) FROM codes WHERE created>=? GROUP BY 1", createdByDay},
		{"SELECT date(updated,'unixepoch','localtime'),COUNT(*) FROM codes WHERE username<>'' AND updated>=? GROUP BY 1", redeemedByDay},
		{"SELECT date(updated,'unixepoch','localtime'),COUNT(*) FROM codes WHERE status='succeeded' AND updated>=? GROUP BY 1", succeededByDay},
	}
	for _, q := range dailyQueries {
		rows, err = tx.Query(q.sql, since)
		if err != nil {
			message(w, 503, "无法读取统计。")
			return
		}
		for rows.Next() {
			var date string
			var count int
			if err = rows.Scan(&date, &count); err != nil {
				rows.Close()
				message(w, 503, "无法读取统计。")
				return
			}
			q.into[date] = count
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			message(w, 503, "无法读取统计。")
			return
		}
	}
	daily := []statsDailyRow{}
	for i := 29; i >= 0; i-- {
		date := today.AddDate(0, 0, -i).Format("2006-01-02")
		daily = append(daily, statsDailyRow{Date: date, Created: createdByDay[date], Redeemed: redeemedByDay[date], Succeeded: succeededByDay[date]})
	}
	stages := []statsStageRow{}
	rows, err = tx.Query("SELECT progress,COUNT(*) FROM codes WHERE status IN ('review','processing') GROUP BY progress ORDER BY progress")
	if err != nil {
		message(w, 503, "无法读取统计。")
		return
	}
	for rows.Next() {
		var stage statsStageRow
		if err = rows.Scan(&stage.Progress, &stage.Count); err != nil {
			rows.Close()
			message(w, 503, "无法读取统计。")
			return
		}
		stages = append(stages, stage)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		message(w, 503, "无法读取统计。")
		return
	}
	if err = tx.Commit(); err != nil {
		message(w, 503, "无法读取一致的统计，请刷新重试。")
		return
	}
	reply(w, 200, map[string]any{
		"codes": codes,
		"rates": map[string]float64{
			"redeemed": rate(codes["redeemed"], codes["total"]),
			"success":  rate(codes["succeeded"], codes["redeemed"]),
		},
		"months":        months,
		"daily":         daily,
		"review_stages": stages,
	})
}
