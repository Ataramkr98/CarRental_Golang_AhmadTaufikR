package services

import (
	"math"
	"time"

	"car-rental-backend/config"
	"car-rental-backend/models"
)

// CarCounts is the fleet breakdown shown on the dashboard.
type CarCounts struct {
	Total       int64 `json:"total"`
	Available   int64 `json:"available"`
	Reserved    int64 `json:"reserved"`
	Rented      int64 `json:"rented"`
	Maintenance int64 `json:"maintenance"`
}

// RentalCounts is the rental breakdown shown on the dashboard.
type RentalCounts struct {
	Total     int64 `json:"total"`
	Pending   int64 `json:"pending"`
	Paid      int64 `json:"paid"`
	Active    int64 `json:"active"`
	Returned  int64 `json:"returned"`
	Cancelled int64 `json:"cancelled"`
}

// PopularCar ranks a vehicle by how often it has been rented.
type PopularCar struct {
	CarID    uint    `json:"car_id"`
	Label    string  `json:"label"`
	Plate    string  `json:"plate"`
	Bookings int64   `json:"bookings"`
	Revenue  float64 `json:"revenue"`
}

// DashboardStats aggregates every metric shown on the admin dashboard.
type DashboardStats struct {
	Cars           CarCounts       `json:"cars"`
	Rentals        RentalCounts    `json:"rentals"`
	Customers      int64           `json:"customers"`
	Revenue        float64         `json:"revenue"`
	LateFees       float64         `json:"late_fees"`
	CashIn         float64         `json:"cash_in"`
	CashOut        float64         `json:"cash_out"`
	NetBalance     float64         `json:"net_balance"`
	Utilization    float64         `json:"utilization"`
	AvgRentalValue float64         `json:"avg_rental_value"`
	RecentRentals  []models.Rental `json:"recent_rentals"`
	PopularCars    []PopularCar    `json:"popular_cars"`
}

// GetDashboardStats builds the dashboard aggregate in a single pass.
func GetDashboardStats() (*DashboardStats, error) {
	stats := &DashboardStats{}

	if err := countByStatus(&models.Car{}, &stats.Cars.Total, map[string]*int64{
		"available":   &stats.Cars.Available,
		"reserved":    &stats.Cars.Reserved,
		"rented":      &stats.Cars.Rented,
		"maintenance": &stats.Cars.Maintenance,
	}); err != nil {
		return nil, err
	}

	if err := countByStatus(&models.Rental{}, &stats.Rentals.Total, map[string]*int64{
		"pending":   &stats.Rentals.Pending,
		"paid":      &stats.Rentals.Paid,
		"active":    &stats.Rentals.Active,
		"returned":  &stats.Rentals.Returned,
		"cancelled": &stats.Rentals.Cancelled,
	}); err != nil {
		return nil, err
	}

	if err := config.DB.Model(&models.Customer{}).Count(&stats.Customers).Error; err != nil {
		return nil, err
	}

	// Revenue counts settled bookings; late fees are reported separately so
	// the two figures can be reconciled against the cash ledger.
	if err := config.DB.Model(&models.Rental{}).
		Where("payment_status = ?", "paid").
		Select("COALESCE(SUM(total_price),0)").Scan(&stats.Revenue).Error; err != nil {
		return nil, err
	}
	if err := config.DB.Model(&models.Rental{}).
		Select("COALESCE(SUM(late_fee),0)").Scan(&stats.LateFees).Error; err != nil {
		return nil, err
	}

	if err := config.DB.Model(&models.CashFlow{}).
		Where("type = ?", "in").
		Select("COALESCE(SUM(amount),0)").Scan(&stats.CashIn).Error; err != nil {
		return nil, err
	}
	if err := config.DB.Model(&models.CashFlow{}).
		Where("type = ?", "out").
		Select("COALESCE(SUM(amount),0)").Scan(&stats.CashOut).Error; err != nil {
		return nil, err
	}
	stats.NetBalance = stats.CashIn - stats.CashOut

	// Utilisation is the share of the fleet currently earning money.
	if stats.Cars.Total > 0 {
		earning := stats.Cars.Rented + stats.Cars.Reserved
		stats.Utilization = round2(float64(earning) / float64(stats.Cars.Total) * 100)
	}

	if stats.Rentals.Total > 0 {
		stats.AvgRentalValue = round2(stats.Revenue / float64(stats.Rentals.Total))
	}

	if err := config.DB.Preload("Car").Preload("Customer").
		Order("id desc").Limit(6).Find(&stats.RecentRentals).Error; err != nil {
		return nil, err
	}
	if stats.RecentRentals == nil {
		stats.RecentRentals = []models.Rental{}
	}

	popular, err := PopularCars(4)
	if err != nil {
		return nil, err
	}
	stats.PopularCars = popular

	return stats, nil
}

// countByStatus counts rows per status value into the supplied pointers.
func countByStatus(model any, total *int64, buckets map[string]*int64) error {
	if err := config.DB.Model(model).Count(total).Error; err != nil {
		return err
	}
	for status, target := range buckets {
		if err := config.DB.Model(model).Where("status = ?", status).Count(target).Error; err != nil {
			return err
		}
	}
	return nil
}

// PopularCars ranks the fleet by booking count and settled revenue.
func PopularCars(limit int) ([]PopularCar, error) {
	type row struct {
		CarID    uint
		Brand    string
		Model    string
		Plate    string
		Bookings int64
		Revenue  float64
	}

	var rows []row
	err := config.DB.Model(&models.Rental{}).
		Select(`rentals.car_id AS car_id, cars.brand AS brand, cars.model AS model,
			cars.plate AS plate, COUNT(rentals.id) AS bookings,
			COALESCE(SUM(CASE WHEN rentals.payment_status = 'paid' THEN rentals.total_price ELSE 0 END), 0) AS revenue`).
		Joins("JOIN cars ON cars.id = rentals.car_id").
		Group("rentals.car_id, cars.brand, cars.model, cars.plate").
		Order("bookings DESC, revenue DESC").
		Limit(limit).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	popular := make([]PopularCar, 0, len(rows))
	for _, item := range rows {
		popular = append(popular, PopularCar{
			CarID: item.CarID, Label: item.Brand + " " + item.Model,
			Plate: item.Plate, Bookings: item.Bookings, Revenue: round2(item.Revenue),
		})
	}
	return popular, nil
}

// RevenuePoint is one bucket in the revenue timeseries.
type RevenuePoint struct {
	Date    string  `json:"date"`
	Label   string  `json:"label"`
	Income  float64 `json:"income"`
	Expense float64 `json:"expense"`
	Net     float64 `json:"net"`
}

// RevenueSeries is the cash-flow chart payload.
type RevenueSeries struct {
	Range        string         `json:"range"`
	Points       []RevenuePoint `json:"points"`
	TotalIncome  float64        `json:"total_income"`
	TotalExpense float64        `json:"total_expense"`
}

// RevenueSeriesForRange buckets ledger entries into daily or monthly points.
//
// Bucketing happens in Go rather than SQL so the query stays portable and the
// chart always renders a continuous axis, including days with no activity.
func RevenueSeriesForRange(rangeKey string) (*RevenueSeries, error) {
	now := time.Now()

	var (
		buckets []RevenuePoint
		step    time.Duration
		monthly bool
	)

	switch rangeKey {
	case "7d", "":
		rangeKey = "7d"
		step = 24 * time.Hour
		buckets = make([]RevenuePoint, 7)
	case "14d":
		step = 24 * time.Hour
		buckets = make([]RevenuePoint, 14)
	case "30d":
		step = 24 * time.Hour
		buckets = make([]RevenuePoint, 30)
	case "6m":
		monthly = true
		buckets = make([]RevenuePoint, 6)
	default:
		return nil, Invalid("range must be one of 7d, 14d, 30d or 6m")
	}

	// Build the axis, newest bucket last.
	keys := make([]string, len(buckets))
	for i := range buckets {
		if monthly {
			point := now.AddDate(0, -(len(buckets) - 1 - i), 0)
			point = time.Date(point.Year(), point.Month(), 1, 0, 0, 0, 0, point.Location())
			keys[i] = point.Format("2006-01")
			buckets[i] = RevenuePoint{Date: keys[i], Label: point.Format("Jan")}
		} else {
			point := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).
				Add(-time.Duration(len(buckets)-1-i) * step)
			keys[i] = point.Format("2006-01-02")
			buckets[i] = RevenuePoint{Date: keys[i], Label: point.Format("2 Jan")}
		}
	}

	// Fetch only the ledger rows inside the window.
	var since time.Time
	if monthly {
		since = now.AddDate(0, -(len(buckets) - 1), 0)
		since = time.Date(since.Year(), since.Month(), 1, 0, 0, 0, 0, since.Location())
	} else {
		since = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).
			Add(-time.Duration(len(buckets)-1) * step)
	}

	var entries []models.CashFlow
	if err := config.DB.Select("type", "amount", "date").
		Where("date >= ?", since).Find(&entries).Error; err != nil {
		return nil, err
	}

	index := make(map[string]int, len(keys))
	for i, key := range keys {
		index[key] = i
	}

	series := &RevenueSeries{Range: rangeKey, Points: buckets}
	for _, entry := range entries {
		key := entry.Date.Format("2006-01-02")
		if monthly {
			key = entry.Date.Format("2006-01")
		}
		position, ok := index[key]
		if !ok {
			continue
		}
		if entry.Type == "in" {
			series.Points[position].Income += entry.Amount
			series.TotalIncome += entry.Amount
		} else {
			series.Points[position].Expense += entry.Amount
			series.TotalExpense += entry.Amount
		}
	}
	for i := range series.Points {
		series.Points[i].Income = round2(series.Points[i].Income)
		series.Points[i].Expense = round2(series.Points[i].Expense)
		series.Points[i].Net = round2(series.Points[i].Income - series.Points[i].Expense)
	}
	series.TotalIncome = round2(series.TotalIncome)
	series.TotalExpense = round2(series.TotalExpense)

	return series, nil
}

// round2 rounds to two decimals so JSON payloads stay tidy.
func round2(value float64) float64 {
	return math.Round(value*100) / 100
}
