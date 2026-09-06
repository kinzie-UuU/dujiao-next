package gormstore

import (
	"fmt"
	"sort"
	"strings"
	"time"

	orderdomain "github.com/dujiao-next/internal/modules/order/domain"

	"github.com/dujiao-next/internal/constants"
	dashboard "github.com/dujiao-next/internal/modules/dashboard/contract"
)

func profitOrderStatuses() []string {
	statuses := append([]string{}, paidOrderStatuses()...)
	return append(statuses, constants.OrderStatusRefunded)
}

// GetProfitOverview 获取利润总览统计
func (r *Store) GetProfitOverview(startAt, endAt time.Time) (dashboard.ProfitOverviewRow, error) {
	result := dashboard.ProfitOverviewRow{}
	if err := r.db.Model(&orderdomain.OrderItem{}).
		Select(`
			COALESCE(SUM(order_items.total_price - order_items.coupon_discount), 0) as total_revenue,
			COALESCE(SUM(CASE WHEN order_items.cost_price > 0 THEN order_items.cost_price * order_items.quantity ELSE 0 END), 0) as total_cost,
			COALESCE(SUM(CASE WHEN order_items.cost_price > 0 THEN 0 ELSE 1 END), 0) as missing_cost_items
		`).
		Joins("JOIN orders ON orders.id = order_items.order_id").
		Where("order_items.deleted_at IS NULL AND orders.deleted_at IS NULL AND orders.created_at >= ? AND orders.created_at < ? AND orders.status IN ?", startAt, endAt, profitOrderStatuses()).
		Scan(&result).Error; err != nil {
		return result, err
	}

	var refundedAmount float64
	if err := r.db.Model(&orderdomain.OrderRefundRecord{}).
		Select("COALESCE(SUM(amount), 0)").
		Where("deleted_at IS NULL AND created_at >= ? AND created_at < ?", startAt, endAt).
		Scan(&refundedAmount).Error; err != nil {
		return result, err
	}
	result.TotalRevenue -= refundedAmount
	return result, nil
}

// GetProfitTrends 获取利润趋势
func (r *Store) GetProfitTrends(startAt, endAt time.Time) ([]dashboard.ProfitTrendRow, error) {
	orderDayExpr := dateGroupExpr(r.db, "orders.created_at", startAt.Location(), startAt)

	rows := make([]dashboard.ProfitTrendRow, 0)
	if err := r.db.Model(&orderdomain.OrderItem{}).Select(fmt.Sprintf(`
		%s as day,
		COALESCE(SUM(order_items.total_price - order_items.coupon_discount), 0) as revenue,
		COALESCE(SUM(CASE WHEN order_items.cost_price > 0 THEN order_items.cost_price * order_items.quantity ELSE 0 END), 0) as cost,
		COALESCE(SUM(CASE WHEN order_items.cost_price > 0 THEN 0 ELSE 1 END), 0) as missing_cost_items
	`, orderDayExpr)).
		Joins("JOIN orders ON orders.id = order_items.order_id").
		Where("order_items.deleted_at IS NULL AND orders.deleted_at IS NULL AND orders.created_at >= ? AND orders.created_at < ? AND orders.status IN ?", startAt, endAt, profitOrderStatuses()).
		Group(orderDayExpr).
		Scan(&rows).Error; err != nil {
		return nil, err
	}

	type refundTrendRow struct {
		Day          string
		RefundAmount float64 `gorm:"column:refund_amount"`
	}
	refundRows := make([]refundTrendRow, 0)
	refundDayExpr := dateGroupExpr(r.db, "created_at", startAt.Location(), startAt)
	if err := r.db.Model(&orderdomain.OrderRefundRecord{}).
		Select(fmt.Sprintf(`
			%s as day,
			COALESCE(SUM(amount), 0) as refund_amount
		`, refundDayExpr)).
		Where("deleted_at IS NULL AND created_at >= ? AND created_at < ?", startAt, endAt).
		Group(refundDayExpr).
		Scan(&refundRows).Error; err != nil {
		return nil, err
	}

	byDay := make(map[string]dashboard.ProfitTrendRow, len(rows)+len(refundRows))
	for _, row := range rows {
		byDay[row.Day] = row
	}
	for _, refundRow := range refundRows {
		day := strings.TrimSpace(refundRow.Day)
		if day == "" {
			continue
		}
		row := byDay[day]
		row.Day = day
		row.Revenue -= refundRow.RefundAmount
		byDay[day] = row
	}

	merged := make([]dashboard.ProfitTrendRow, 0, len(byDay))
	for _, row := range byDay {
		merged = append(merged, row)
	}
	sort.Slice(merged, func(i, j int) bool {
		return merged[i].Day < merged[j].Day
	})
	return merged, nil
}
