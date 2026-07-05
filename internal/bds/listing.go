package bds

import (
	"fmt"
	"time"
)

// Listing represents one real estate listing scraped from a source.
type Listing struct {
	ID          string    // SHA-1 of URL (dedup key)
	Title       string
	Price       int64     // VND, 0 = unknown
	Area        float64   // m², 0 = unknown
	Location    string
	URL         string
	Source      string    // "batdongsan" | "nhatot" | "muaban"
	CrawledAt   time.Time
}

// PriceDisplay formats VND into human-readable tỷ/triệu.
func (l Listing) PriceDisplay() string {
	if l.Price <= 0 {
		return "Giá TL"
	}
	if l.Price >= 1_000_000_000 {
		return fmt.Sprintf("%.2f tỷ", float64(l.Price)/1_000_000_000)
	}
	return fmt.Sprintf("%.0f triệu", float64(l.Price)/1_000_000)
}

// AreaDisplay formats m².
func (l Listing) AreaDisplay() string {
	if l.Area <= 0 {
		return "?"
	}
	return fmt.Sprintf("%.0fm²", l.Area)
}

// PricePerM2 returns price per m², 0 if either value is missing.
func (l Listing) PricePerM2() int64 {
	if l.Area <= 0 || l.Price <= 0 {
		return 0
	}
	return int64(float64(l.Price) / l.Area)
}

// SearchParams filters listings on read.
type SearchParams struct {
	MaxPrice int64
	MinArea  float64
	Source   string
}
