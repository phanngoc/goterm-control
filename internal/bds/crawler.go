package bds

import (
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ─── Source definitions ───────────────────────────────────────────────────────

// Source describes one real-estate website to crawl.
type Source struct {
	Name     string
	UseCDP   bool                                   // true = use Chrome CDP (Cloudflare bypass)
	FetchFn  func(url string) (string, error)       // optional custom fetcher; overrides UseCDP
	PageURLs func(pages int, budget int64) []string // builds list of URLs to fetch
	Parse    func(body string) []Listing            // extracts listings from HTML
}

// AllSources returns all built-in sources.
// HTTP sources (inhadat, alonhadat, chotot) are Cloudflare-free.
// CDP sources (batdongsan, muaban, homedy) use a real Chrome to bypass CF.
func AllSources() []Source {
	return []Source{
		inhadat(),
		alonhadat(),
		chotot(),
		batdongsan(),
		nhatot(),
		muaban(),
		homedy(),
	}
}

// SourceByName returns sources matching the comma-separated name list.
// "all" returns everything.
// "http" returns only HTTP sources (no Chrome required).
func SourceByName(names string) []Source {
	all := AllSources()
	if names == "all" {
		return all
	}
	if names == "http" {
		var out []Source
		for _, s := range all {
			if !s.UseCDP && s.FetchFn == nil {
				out = append(out, s)
			}
		}
		// include custom-fetcher sources (they're also HTTP-based)
		for _, s := range all {
			if s.FetchFn != nil {
				out = append(out, s)
			}
		}
		return out
	}
	wanted := map[string]bool{}
	for n := range strings.SplitSeq(names, ",") {
		wanted[strings.TrimSpace(n)] = true
	}
	var out []Source
	for _, s := range all {
		if wanted[s.Name] {
			out = append(out, s)
		}
	}
	return out
}

// ─── Crawler orchestrator ─────────────────────────────────────────────────────

// CrawlAll runs all sources concurrently and returns deduplicated listings.
func CrawlAll(sources []Source, budget int64, pages int) []Listing {
	var (
		mu      sync.Mutex
		seen    = map[string]bool{}
		results []Listing
		wg      sync.WaitGroup
	)

	for _, src := range sources {
		urls := src.PageURLs(pages, budget)
		wg.Add(len(urls))
		for _, u := range urls {
			go func() {
				defer wg.Done()
				var body string
				var err error
				switch {
				case src.FetchFn != nil:
					body, err = src.FetchFn(u)
				case src.UseCDP:
					body, err = fetchCDP(u)
				default:
					body, err = fetch(u)
				}
				if err != nil {
					fmt.Printf("[%s] fetch error %s: %v\n", src.Name, u, err)
					return
				}
				listings := src.Parse(body)
				mu.Lock()
				for i := range listings {
					l := &listings[i]
					l.Source = src.Name
					if l.ID == "" {
						l.ID = urlID(l.URL)
					}
					if seen[l.URL] {
						continue
					}
					seen[l.URL] = true
					// budget filter (skip if price known and over budget)
					if l.Price > 0 && budget > 0 && l.Price > budget {
						continue
					}
					results = append(results, *l)
				}
				mu.Unlock()
				fmt.Printf("[%s] %s → %d listings\n", src.Name, u, len(listings))
			}()
		}
	}
	wg.Wait()
	return results
}

// ─── HTTP client ─────────────────────────────────────────────────────────────

var httpClient = &http.Client{Timeout: 20 * time.Second}

var browserHeaders = map[string]string{
	"User-Agent":      "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
	"Accept":          "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
	"Accept-Language": "vi-VN,vi;q=0.9,en-US;q=0.8",
	"Accept-Encoding": "identity",
	"Cache-Control":   "no-cache",
}

func fetch(url string) (string, error) {
	// polite delay: 1-3 seconds random
	time.Sleep(time.Duration(1000+rand.Intn(2000)) * time.Millisecond)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
	for k, v := range browserHeaders {
		req.Header.Set(k, v)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20)) // 4 MB cap
	return string(b), err
}

// ─── Common extractors ────────────────────────────────────────────────────────

// parsePriceVND extracts the first VND price found in text.
// Understands: "2 tỷ", "1.5 tỷ", "800 triệu", "2,500,000,000", "2.5 tỷ đồng"
func parsePriceVND(s string) int64 {
	s = strings.ToLower(s)

	// tỷ pattern: "2.5 tỷ", "1,5 tỷ"
	reTy := regexp.MustCompile(`([\d][,\d]*(?:[.,]\d+)?)\s*tỷ`)
	if m := reTy.FindStringSubmatch(s); len(m) > 1 {
		return int64(parseFloat(m[1]) * 1_000_000_000)
	}

	// triệu pattern: "800 triệu", "1,200 triệu"
	reTr := regexp.MustCompile(`([\d][,\d]*(?:[.,]\d+)?)\s*triệu`)
	if m := reTr.FindStringSubmatch(s); len(m) > 1 {
		return int64(parseFloat(m[1]) * 1_000_000)
	}

	// raw number ≥ 100 million (e.g. "2,500,000,000")
	reNum := regexp.MustCompile(`[\d]{3,}(?:[.,][\d]+)*`)
	if m := reNum.FindString(s); m != "" {
		clean := strings.ReplaceAll(strings.ReplaceAll(m, ".", ""), ",", "")
		if v, err := strconv.ParseInt(clean, 10, 64); err == nil && v >= 100_000_000 {
			return v
		}
	}
	return 0
}


// parseFloat normalises Vietnamese numeric strings to float64.
// Handles all separator conventions:
//   - "299,3"   → 299.3  (comma = decimal, no thousands)
//   - "1.021,9" → 1021.9 (dot = thousands, comma = decimal)
//   - "1,021.9" → 1021.9 (comma = thousands, dot = decimal)
//   - "1.500"   → 1500   (dot = thousands, no decimal)
//   - "1,500"   → 1500   (comma = thousands, no decimal)
func parseFloat(s string) float64 {
	s = strings.TrimSpace(strings.ReplaceAll(s, " ", ""))
	if s == "" {
		return 0
	}
	dotIdx := strings.LastIndex(s, ".")
	commaIdx := strings.LastIndex(s, ",")

	switch {
	case dotIdx > 0 && commaIdx > 0:
		// Both separators present: whichever comes last is the decimal separator.
		if commaIdx > dotIdx {
			// "1.021,9" → dot=thousands, comma=decimal
			s = strings.ReplaceAll(s, ".", "") // remove thousands dots
			s = strings.ReplaceAll(s, ",", ".") // comma → decimal point
		} else {
			// "1,021.9" → comma=thousands, dot=decimal (standard)
			s = strings.ReplaceAll(s, ",", "")
		}
	case commaIdx > 0:
		// Only comma: decimal if ≤2 digits after, else thousands
		after := s[commaIdx+1:]
		if len(after) <= 2 {
			s = strings.ReplaceAll(s, ",", ".")
		} else {
			s = strings.ReplaceAll(s, ",", "")
		}
	case dotIdx > 0:
		// Only dot: decimal if ≤2 digits after, else thousands
		after := s[dotIdx+1:]
		if len(after) > 2 {
			s = strings.ReplaceAll(s, ".", "")
		}
	}
	v, _ := strconv.ParseFloat(s, 64)
	return v
}


// stripTags removes HTML tags from a string.
func stripTags(s string) string {
	re := regexp.MustCompile(`<[^>]+>`)
	return strings.TrimSpace(re.ReplaceAllString(s, " "))
}

// normalizeSpace collapses whitespace.
func normalizeSpace(s string) string {
	re := regexp.MustCompile(`\s+`)
	return strings.TrimSpace(re.ReplaceAllString(s, " "))
}

// ─── i-nhadat.com ────────────────────────────────────────────────────────────
// Accessible (Microsoft-IIS, no Cloudflare). 20 listings/page.
// URL pattern: /can-ban-nha-dat/quang-nam-t47.htm?page=N&gia=16 (gia=16 → ≤2 tỷ)

func inhadat() Source {
	return Source{
		Name: "inhadat",
		PageURLs: func(pages int, budget int64) []string {
			urls := make([]string, pages)
			for i := range urls {
				// gia=16 = price bucket "dưới 2 tỷ" on i-nhadat
				urls[i] = fmt.Sprintf(
					"https://i-nhadat.com/can-ban-nha-dat/quang-nam-t47.htm?page=%d&gia=16", i+1)
			}
			return urls
		},
		Parse: parseInhadat,
	}
}

// parseInhadat parses i-nhadat.com listing pages.
// Confirmed structure (2026-04):
//
//	<div class='content-item'>
//	  <div class='ct_title'><a href='/slug-ID.html'>TITLE</a></div>
//	  <div class='ct_price'>1,55 tỷ </div>
//	  <div class='ct_dt'><label>Diện tích:</label> 160 m<sup>2</sup></div>
//	  <div class='ct_dis'><label>Địa chỉ:</label> Phường..., TP...</div>
//	</div>
func parseInhadat(body string) []Listing {
	var listings []Listing

	// Split on card boundary
	const cardMarker = "class='content-item'"
	parts := strings.Split(body, cardMarker)
	if len(parts) < 2 {
		return nil
	}

	for _, chunk := range parts[1:] { // skip text before first card
		l := Listing{}

		// URL (relative) and title
		if m := regexp.MustCompile(`class='ct_title'><a href='(/[^']+)'[^>]*>([^<]+)`).
			FindStringSubmatch(chunk); len(m) > 2 {
			l.URL = "https://i-nhadat.com" + m[1]
			l.Title = normalizeSpace(m[2])
		}
		if l.URL == "" {
			continue
		}

		// Price: "1,55 tỷ" or "800 triệu" etc.
		// Skip price-per-m² entries (contain "/m" or "/nbsp;m").
		if m := regexp.MustCompile(`class='ct_price'>([^<]+)`).
			FindStringSubmatch(chunk); len(m) > 1 {
			raw := m[1]
			if !strings.Contains(raw, "/") { // "/m²" or "/&nbsp;m" = per-m² price
				l.Price = parsePriceVND(raw)
			}
		}

		// Area: "160 m<sup>2"  →  take number before " m"
		if m := regexp.MustCompile(`Diện tích:</label>\s*([\d,\.]+)\s*m`).
			FindStringSubmatch(chunk); len(m) > 1 {
			l.Area = parseFloat(m[1])
		}

		// Location
		if m := regexp.MustCompile(`ct_dis'><label>[^<]+</label>([^<]+)`).
			FindStringSubmatch(chunk); len(m) > 1 {
			l.Location = normalizeSpace(stripTags(m[1]))
		}

		listings = append(listings, l)
	}
	return listings
}

// ─── alonhadat.com.vn ─────────────────────────────────────────────────────────
// Accessible (Microsoft-IIS, no Cloudflare). 20 listings/page.
// Uses schema.org microdata — price is in itemprop='price' content='NNNN'.

func alonhadat() Source {
	return Source{
		Name: "alonhadat",
		PageURLs: func(pages int, budget int64) []string {
			// Active Quảng Nam districts ordered by investment interest
			bases := []string{
				"https://alonhadat.com.vn/nha-dat/can-ban/nha-dat/quang-nam/362/thanh-pho-hoi-an.html",
				"https://alonhadat.com.vn/nha-dat/can-ban/nha-dat/quang-nam/363/thi-xa-dien-ban.html",
				"https://alonhadat.com.vn/nha-dat/can-ban/nha-dat/quang-nam/361/thanh-pho-tam-ky.html",
				"https://alonhadat.com.vn/nha-dat/can-ban/nha-dat/quang-nam/367/huyen-nui-thanh.html",
				"https://alonhadat.com.vn/nha-dat/can-ban/nha-dat/quang-nam/377/huyen-duy-xuyen.html",
				"https://alonhadat.com.vn/nha-dat/can-ban/nha-dat/quang-nam/364/huyen-thang-binh.html",
				"https://alonhadat.com.vn/nha-dat/can-ban/nha-dat/quang-nam/374/huyen-dai-loc.html",
			}
			var urls []string
			for _, base := range bases {
				for p := 1; p <= pages; p++ {
					if p == 1 {
						urls = append(urls, base)
					} else {
						urls = append(urls, fmt.Sprintf("%s?page=%d", base, p))
					}
				}
			}
			return urls
		},
		Parse: parseAlonhadat,
	}
}

// parseAlonhadat parses alonhadat.com.vn listing pages.
// Confirmed structure (2026-04):
//
//	<div class='property-item' itemscope itemtype='https://schema.org/RealEstateListing'>
//	  <a class='link' href='/slug-ID.html' itemprop='url'>
//	    <h3 class='property-title' itemprop='name'>TITLE</h3>
//	  </a>
//	  <time class='created-date' datetime='2026-04-11'>
//	  <span class='price' itemprop='offers' ...>
//	    <span itemprop='price' content='32000000000'>32 tỷ</span>
//	  </span>
//	  <span class='area' itemprop='floorSize' ...>
//	    <span itemprop='value' content='380'>380</span> m²
//	  </span>
//	  <span class='new-address'>Phường..., TP Hội An</span>
//	</div>
func parseAlonhadat(body string) []Listing {
	var listings []Listing

	const cardMarker = "class='property-item'"
	parts := strings.Split(body, cardMarker)
	if len(parts) < 2 {
		return nil
	}

	for _, chunk := range parts[1:] {
		l := Listing{}

		// URL + title via itemprop
		if m := regexp.MustCompile(`href='(/[^']+)'\s+itemprop='url'`).
			FindStringSubmatch(chunk); len(m) > 1 {
			l.URL = "https://alonhadat.com.vn" + m[1]
		}
		if l.URL == "" {
			continue
		}
		if m := regexp.MustCompile(`itemprop='name'>([^<]+)</h3>`).
			FindStringSubmatch(chunk); len(m) > 1 {
			l.Title = normalizeSpace(m[1])
		}

		// Price from content attribute (already numeric VND)
		if m := regexp.MustCompile(`itemprop='price'\s+content='(\d+)'`).
			FindStringSubmatch(chunk); len(m) > 1 {
			l.Price, _ = strconv.ParseInt(m[1], 10, 64)
		}

		// Area: <span itemprop='value'>560</span>  (inside class='area' block)
		// Use the value span that comes after 'floorSize'
		if m := regexp.MustCompile(`floorSize[^>]*>[\s\S]{0,200}itemprop='value'>([\d,\.]+)</span>`).
			FindStringSubmatch(chunk); len(m) > 1 {
			l.Area = parseFloat(m[1])
		}

		// Location
		if m := regexp.MustCompile(`class='new-address'>([^<]+)`).
			FindStringSubmatch(chunk); len(m) > 1 {
			l.Location = normalizeSpace(m[1])
		}

		listings = append(listings, l)
	}
	return listings
}

// ─── batdongsan.com.vn (CDP) ──────────────────────────────────────────────────

func batdongsan() Source {
	return Source{
		Name:   "batdongsan",
		UseCDP: true,
		PageURLs: func(pages int, budget int64) []string {
			urls := make([]string, pages)
			for i := range urls {
				urls[i] = fmt.Sprintf(
					"https://batdongsan.com.vn/ban-dat-quang-nam?gia=0-3&p=%d", i+1)
			}
			return urls
		},
		Parse: parseBatdongsan,
	}
}

// parseBatdongsan handles batdongsan.com.vn CDP-rendered HTML.
// Confirmed card structure (2026-04):
//
//	class="js__card js__card-full-web js__card-listing pr-container re__card-full ..."
//	  <a href="/ban-dat-..." title="FULL TITLE">
//	  <span class="pr-title js__card-title" product-title="">SHORT TITLE</span>
//	  <span class="re__card-config-price js__card-config-item">1,5 tỷ</span>
//	  <span class="re__card-config-area js__card-config-item">299,3 m²</span>
//	  <div class="re__card-location"><span>TP. Hội An (...)</span></div>
func parseBatdongsan(body string) []Listing {
	var listings []Listing

	// Split on the unique full card class string
	const cardMarker = "js__card-full-web"
	parts := strings.Split(body, cardMarker)
	if len(parts) < 2 {
		return nil
	}

	for _, chunk := range parts[1:] {
		l := Listing{}

		// URL + full title from the main anchor's href and title= attributes
		if m := regexp.MustCompile(`href="(/ban-dat[^"]+)"\s+title="([^"]+)"`).
			FindStringSubmatch(chunk); len(m) > 2 {
			l.URL = "https://batdongsan.com.vn" + m[1]
			l.Title = m[2]
		}
		if l.URL == "" {
			continue
		}

		// Price: "Giá thỏa thuận" → 0; "1,5 tỷ" → parsed
		if m := regexp.MustCompile(`re__card-config-price[^>]+>([^<]+)<`).
			FindStringSubmatch(chunk); len(m) > 1 {
			l.Price = parsePriceVND(m[1])
		}

		// Area: "299,3 m²" — note comma decimal separator
		if m := regexp.MustCompile(`re__card-config-area[^>]+>([\d][,\.\d]*)\s*m`).
			FindStringSubmatch(chunk); len(m) > 1 {
			if a := parseFloat(m[1]); a >= 5 { // ignore stray single-digit matches
				l.Area = a
			}
		}

		// Location: first <span> text inside re__card-location div
		if m := regexp.MustCompile(`re__card-location[^>]*>[\s\S]{0,150}<span>([^<]+)</span>`).
			FindStringSubmatch(chunk); len(m) > 1 {
			l.Location = normalizeSpace(m[1])
		}

		listings = append(listings, l)
	}
	return listings
}

// ─── nhatot.com (Chotot JSON API) ────────────────────────────────────────────
// Uses the public Chotot gateway API — no CDP required.
// region_v2=3016 = Quảng Nam; cg=1000 = bất động sản.

func nhatot() Source {
	// Custom fetcher sets Origin/Referer so the gateway allows the request.
	fetchNhatot := func(apiURL string) (string, error) {
		time.Sleep(time.Duration(500+rand.Intn(1000)) * time.Millisecond)
		req, err := http.NewRequest("GET", apiURL, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("Origin", "https://www.nhatot.com")
		req.Header.Set("Referer", "https://www.nhatot.com/")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", browserHeaders["User-Agent"])
		resp, err := httpClient.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return "", fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		return string(b), err
	}

	return Source{
		Name:    "nhatot",
		FetchFn: fetchNhatot,
		PageURLs: func(pages int, budget int64) []string {
			priceMax := budget
			if priceMax <= 0 {
				priceMax = 2_000_000_000
			}
			urls := make([]string, pages)
			for i := range urls {
				urls[i] = fmt.Sprintf(
					"https://gateway.chotot.com/v1/public/ad-listing?cg=1000&region_v2=3016&price_max=%d&limit=20&page=%d",
					priceMax, i+1)
			}
			return urls
		},
		Parse: parseNhatotJSON,
	}
}

// parseNhatotJSON parses the Chotot gateway JSON response.
// Relevant fields per ad: ad_id, subject, price, area, area_name, short_address.
func parseNhatotJSON(body string) []Listing {
	var resp struct {
		Ads []struct {
			AdID         int64   `json:"ad_id"`
			Subject      string  `json:"subject"`
			Price        int64   `json:"price"`
			Size         float64 `json:"size"`         // actual m² (area = district code)
			AreaName     string  `json:"area_name"`
			ShortAddress string  `json:"short_address"`
		} `json:"ads"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		return nil
	}
	listings := make([]Listing, 0, len(resp.Ads))
	for _, ad := range resp.Ads {
		if ad.AdID == 0 {
			continue
		}
		loc := ad.AreaName
		if ad.ShortAddress != "" {
			loc = ad.ShortAddress
		}
		listings = append(listings, Listing{
			Title:    ad.Subject,
			Price:    ad.Price,
			Area:     ad.Size,
			Location: loc,
			URL:      fmt.Sprintf("https://www.nhatot.com/a/%d.htm", ad.AdID),
		})
	}
	return listings
}

// ─── chotot.com – land category (JSON API) ───────────────────────────────────
// Same Chotot gateway as nhatot but uses cg=1040 (Đất – land only).
// No Cloudflare, just needs Origin/Referer headers.

func chotot() Source {
	fetchFn := func(apiURL string) (string, error) {
		time.Sleep(time.Duration(500+rand.Intn(1000)) * time.Millisecond)
		req, err := http.NewRequest("GET", apiURL, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("Origin", "https://www.chotot.com")
		req.Header.Set("Referer", "https://www.chotot.com/")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", browserHeaders["User-Agent"])
		resp, err := httpClient.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return "", fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		return string(b), err
	}

	return Source{
		Name:    "chotot",
		FetchFn: fetchFn,
		PageURLs: func(pages int, budget int64) []string {
			priceMax := budget
			if priceMax <= 0 {
				priceMax = 2_000_000_000
			}
			urls := make([]string, pages)
			for i := range urls {
				// cg=1040 = Đất (land); region_v2=3016 = Quảng Nam
				urls[i] = fmt.Sprintf(
					"https://gateway.chotot.com/v1/public/ad-listing?cg=1040&region_v2=3016&price_max=%d&limit=20&page=%d",
					priceMax, i+1)
			}
			return urls
		},
		Parse: parseChotot,
	}
}

// parseChotot parses the Chotot gateway JSON (land category).
// Same schema as nhatot but category_name = "Đất".
func parseChotot(body string) []Listing {
	var resp struct {
		Ads []struct {
			AdID         int64   `json:"ad_id"`
			Subject      string  `json:"subject"`
			Price        int64   `json:"price"`
			Size         float64 `json:"size"`
			AreaName     string  `json:"area_name"`
			ShortAddress string  `json:"short_address"`
		} `json:"ads"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		return nil
	}
	listings := make([]Listing, 0, len(resp.Ads))
	for _, ad := range resp.Ads {
		if ad.AdID == 0 {
			continue
		}
		loc := ad.AreaName
		if ad.ShortAddress != "" {
			loc = ad.ShortAddress
		}
		listings = append(listings, Listing{
			Title:    ad.Subject,
			Price:    ad.Price,
			Area:     ad.Size,
			Location: loc,
			URL:      fmt.Sprintf("https://www.chotot.com/a/%d.htm", ad.AdID),
		})
	}
	return listings
}

// ─── homedy.com (CDP) ────────────────────────────────────────────────────────
// homedy.com is a React SPA — listings are JS-rendered, requires Chrome CDP.

func homedy() Source {
	return Source{
		Name:   "homedy",
		UseCDP: true,
		PageURLs: func(pages int, budget int64) []string {
			urls := make([]string, pages)
			for i := range urls {
				if i == 0 {
					urls[i] = "https://homedy.com/ban-dat-quang-nam"
				} else {
					urls[i] = fmt.Sprintf("https://homedy.com/ban-dat-quang-nam?page=%d", i+1)
				}
			}
			return urls
		},
		Parse: parseHomedy,
	}
}

// parseHomedy extracts listings from homedy.com's server-side rendered HTML.
// Card structure (confirmed 2026-04):
//
//	<div class="product-item" data-id="NNNN">
//	  <a href="/slug-NNNN" class="product-url">
//	    <h3 class="product-title">TITLE</h3>
//	  </a>
//	  <div class="product-price">1,5 tỷ</div>
//	  <div class="product-area">120 m²</div>
//	  <div class="product-address">Phường..., TP Hội An</div>
//	</div>
func parseHomedy(body string) []Listing {
	var listings []Listing

	const cardMarker = `class="product-item"`
	parts := strings.Split(body, cardMarker)
	if len(parts) < 2 {
		return nil
	}

	for _, chunk := range parts[1:] {
		l := Listing{}

		// URL slug: href="/slug"
		if m := regexp.MustCompile(`href="(/[^"]{5,100})"\s+class="product-url"`).
			FindStringSubmatch(chunk); len(m) > 1 {
			l.URL = "https://homedy.com" + m[1]
		}
		// Fallback: any first short href in chunk
		if l.URL == "" {
			if m := regexp.MustCompile(`href="(/[a-z0-9][^"]{5,80})"`).
				FindStringSubmatch(chunk); len(m) > 1 {
				l.URL = "https://homedy.com" + m[1]
			}
		}
		if l.URL == "" {
			continue
		}

		// Title
		if m := regexp.MustCompile(`class="product-title">([^<]+)</h`).
			FindStringSubmatch(chunk); len(m) > 1 {
			l.Title = normalizeSpace(m[1])
		}

		// Price
		if m := regexp.MustCompile(`class="product-price">([^<]+)<`).
			FindStringSubmatch(chunk); len(m) > 1 {
			l.Price = parsePriceVND(m[1])
		}

		// Area
		if m := regexp.MustCompile(`class="product-area">([\d,\.]+)\s*m`).
			FindStringSubmatch(chunk); len(m) > 1 {
			l.Area = parseFloat(m[1])
		}

		// Location
		if m := regexp.MustCompile(`class="product-address">([^<]+)<`).
			FindStringSubmatch(chunk); len(m) > 1 {
			l.Location = normalizeSpace(m[1])
		}

		listings = append(listings, l)
	}
	return listings
}

// ─── muaban.net (CDP) ─────────────────────────────────────────────────────────

func muaban() Source {
	return Source{
		Name:   "muaban",
		UseCDP: true,
		PageURLs: func(pages int, budget int64) []string {
			urls := make([]string, pages)
			for i := range urls {
				urls[i] = fmt.Sprintf("https://muaban.net/bat-dong-san/quang-nam?page=%d", i+1)
			}
			return urls
		},
		Parse: parseMuaban,
	}
}

// parseMuaban extracts listings from the __NEXT_DATA__ JSON block embedded in
// the CDP-rendered muaban.net page (Next.js SSR).
// Structure: props.pageProps.classified.items[]
//   - title, price (numeric VND), url (relative), location, attributes[].value (area string)
func parseMuaban(body string) []Listing {
	re := regexp.MustCompile(`<script id="__NEXT_DATA__" type="application/json">([\s\S]+?)</script>`)
	m := re.FindStringSubmatch(body)
	if len(m) < 2 {
		return nil
	}

	var root struct {
		Props struct {
			PageProps struct {
				Classified struct {
					Items []struct {
						ID         float64 `json:"id"`
						Title      string  `json:"title"`
						Price      float64 `json:"price"`
						URL        string  `json:"url"`
						Location   string  `json:"location"`
						Attributes []struct {
							Value string `json:"value"`
						} `json:"attributes"`
					} `json:"items"`
				} `json:"classified"`
			} `json:"pageProps"`
		} `json:"props"`
	}
	if err := json.Unmarshal([]byte(m[1]), &root); err != nil {
		return nil
	}

	items := root.Props.PageProps.Classified.Items
	listings := make([]Listing, 0, len(items))
	for _, item := range items {
		if item.ID == 0 || item.URL == "" {
			continue
		}
		l := Listing{
			Title:    item.Title,
			Price:    int64(item.Price),
			Location: item.Location,
			URL:      "https://muaban.net" + item.URL,
		}
		// Area string is in attributes, e.g. "700 m²" or "7,000 m²"
		areaRe := regexp.MustCompile(`([\d,\.]+)\s*m`)
		for _, attr := range item.Attributes {
			if am := areaRe.FindStringSubmatch(attr.Value); len(am) > 1 {
				l.Area = parseFloat(am[1])
				break
			}
		}
		listings = append(listings, l)
	}
	return listings
}
