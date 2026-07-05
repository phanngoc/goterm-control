// bdscrawler – CLI tool to crawl BĐS Quảng Nam listings and save to SQLite.
//
// Usage:
//
//	bdscrawler [flags]
//
// Sources:
//
//	inhadat    – i-nhadat.com     (HTTP, no Cloudflare)
//	alonhadat  – alonhadat.com.vn (HTTP, no Cloudflare)
//	batdongsan – batdongsan.com.vn (CDP, Cloudflare bypass via Chrome)
//	nhatot     – nhatot.com        (CDP, Cloudflare bypass via Chrome)
//	muaban     – muaban.net        (CDP, Cloudflare bypass via Chrome)
//	all        – all of the above
package main

import (
	"flag"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/ngocp/goterm-control/internal/bds"
)

func main() {
	budget := flag.Int64("budget", 2_000_000_000, "max budget in VND")
	sources := flag.String("sources", "all",
		"sources: inhadat,alonhadat,batdongsan,nhatot,muaban,all")
	pages := flag.Int("pages", 3, "pages to crawl per source")
	dbPath := flag.String("db", "./bds.db", "SQLite database path")
	list := flag.Bool("list", false, "list saved listings (skip crawl)")
	minArea := flag.Float64("min-area", 0, "minimum area in m²")
	flag.Parse()

	// Chrome process must be shut down cleanly on exit
	defer bds.CloseCDP()

	db, err := bds.OpenDB(*dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open db: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	if !*list {
		srcs := bds.SourceByName(*sources)
		if len(srcs) == 0 {
			fmt.Fprintf(os.Stderr, "no sources matched %q\n", *sources)
			os.Exit(1)
		}

		cdpSrcs := 0
		for _, s := range srcs {
			if s.UseCDP {
				cdpSrcs++
			}
		}
		if cdpSrcs > 0 {
			fmt.Printf("Note: %d source(s) use Chrome CDP (Cloudflare bypass) – browser will start.\n", cdpSrcs)
		}
		fmt.Printf("Crawling %d source(s), up to %d page(s) each, budget ≤ %s ...\n",
			len(srcs), *pages, fmtVND(*budget))

		listings := bds.CrawlAll(srcs, *budget, *pages)
		saved := 0
		for i := range listings {
			if err := db.Save(&listings[i]); err == nil {
				saved++
			}
		}
		fmt.Printf("\nFetched %d listings → %d new saved to %s (total in DB: %d)\n\n",
			len(listings), saved, *dbPath, db.Count())
	}

	results, err := db.Search(bds.SearchParams{
		MaxPrice: *budget,
		MinArea:  *minArea,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "search: %v\n", err)
		os.Exit(1)
	}
	if len(results) == 0 {
		fmt.Println("No listings found.")
		return
	}

	printTable(results)
}

func printTable(listings []bds.Listing) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "#\tGiá\tDT\tGiá/m²\tNguồn\tTiêu đề\tURL")
	fmt.Fprintln(w, "-\t----\t---\t-------\t------\t------\t---")
	for i, l := range listings {
		p2 := ""
		if l.PricePerM2() > 0 {
			p2 = fmtVND(l.PricePerM2()) + "/m²"
		}
		title := l.Title
		if len(title) > 50 {
			title = title[:47] + "..."
		}
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n",
			i+1,
			l.PriceDisplay(),
			l.AreaDisplay(),
			p2,
			l.Source,
			title,
			l.URL,
		)
	}
	w.Flush()
	fmt.Printf("\nTotal: %d listings\n", len(listings))
}

func fmtVND(v int64) string {
	if v >= 1_000_000_000 {
		return fmt.Sprintf("%.2ftỷ", float64(v)/1_000_000_000)
	}
	return fmt.Sprintf("%.0ftriệu", float64(v)/1_000_000)
}
