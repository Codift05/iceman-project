package payment_test

import "time"

// waktuTetap memberi acuan waktu yang tidak berubah antar pemanggilan, supaya
// uji yang membandingkan batas waktu tidak bergantung pada kapan ia berjalan.
func waktuTetap() time.Time {
	return time.Date(2026, 10, 5, 12, 0, 0, 0, time.Local)
}
