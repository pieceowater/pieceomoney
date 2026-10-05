package svc

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	analyticssvc "pieceomoney/internal/pkg/analytics/svc"
	ledgersvc "pieceomoney/internal/pkg/ledger/svc"
	"pieceomoney/internal/pkg/storage/repo"
)

// isNewMerchant reports whether t is the first payment to its merchant.
func (s *Service) isNewMerchant(ctx context.Context, t repo.Transaction) bool {
	if t.Merchant == "" {
		return false
	}
	n, err := s.txs.MerchantPayments(ctx, t.Merchant, t.ID)
	if err != nil {
		s.logger.Error("merchant history lookup failed", slog.Any("error", err))
		return false
	}
	return n == 0
}

// unusualAlert warns about a payment far above the usual size, judged
// against the last 90 days of payments in the same currency.
func (s *Service) unusualAlert(ctx context.Context, t repo.Transaction) string {
	if t.AmountMinor <= 0 {
		return ""
	}
	now := time.Now()
	history, err := s.txs.Between(ctx, now.AddDate(0, 0, -90), now.Add(time.Hour))
	if err != nil {
		s.logger.Error("history lookup failed", slog.Any("error", err))
		return ""
	}
	var amounts []int64
	for _, h := range history {
		if h.ID != t.ID && h.Currency == t.Currency && h.AmountMinor > 0 {
			amounts = append(amounts, h.AmountMinor)
		}
	}
	thr, ok := analyticssvc.UnusualThreshold(amounts, s.cfg.UnusualMultiplier)
	if !ok || t.AmountMinor < thr {
		return ""
	}
	return fmt.Sprintf("⚠️ <b>Необычно крупная трата:</b> %s %s\nОбычно такие платежи заметно меньше (порог ≈ %s).",
		ledgersvc.FormatAmount(t.AmountMinor), t.Currency, ledgersvc.FormatAmount(thr))
}

// dailyAlert fires once, on the payment that brings today's count to the
// configured threshold.
func (s *Service) dailyAlert(ctx context.Context, t repo.Transaction) string {
	limit := s.cfg.DailyPaymentsWarn
	if limit <= 0 {
		return ""
	}
	now := time.Now().In(s.cfg.Location)
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, s.cfg.Location)
	if t.TS.Before(dayStart) {
		return ""
	}
	today, err := s.txs.Between(ctx, dayStart, dayStart.AddDate(0, 0, 1))
	if err != nil {
		s.logger.Error("daily count failed", slog.Any("error", err))
		return ""
	}
	if len(today) != limit {
		return ""
	}
	return fmt.Sprintf("⚠️ <b>Сегодня уже %d оплат.</b> Может, пора притормозить?", limit)
}
