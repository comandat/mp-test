package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
)

type ParsedEmail struct {
	Kind           string // confirmation | shipped | arrived
	OrderNumber    string
	Products       []Product
	TotalBani      int64
	EasyboxName    string
	EasyboxAddress string
	PickupDeadline *time.Time
	PinCode        string
	QRURL          string
}

var (
	reConfirmationSubject = regexp.MustCompile(`(?i)Confirmare\s+(?:înregistrare|inregistrare)\s+comand[ăa]\s*#?\s*(\d+)`)
	reShippedSubject      = regexp.MustCompile(`(?i)Comanda\s+ta\s*#?\s*(\d+)\s+a\s+fost\s+predat[ăa]\s+curierului`)
	// Arrival: "Comanda ta eMAG numărul X a ajuns în easybox Y"
	reArrivedBody = regexp.MustCompile(`(?i)Comanda\s+ta\s+eMAG\s+num[ăa]rul\s+(\d+)\s+a\s+ajuns\s+[îi]n\s+([^.\n\r<]+)`)
	// Marketplace arrival (skipped — not the products user cares about):
	// "Comanda ta eMAG Marketplace - SELLER,eMAG numărul X a ajuns ..."
	reMarketplaceArrived = regexp.MustCompile(`(?i)Comanda\s+ta\s+eMAG\s+Marketplace\s*-[^\n]*,\s*eMAG\s+num[ăa]rul`)
	// Reminder (kept regardless of marketplace — it means the package is sitting in
	// the easybox close to expiry and the user needs to act):
	// "coletul eMAG [Marketplace - SELLER,eMAG] numărul X te mai așteaptă până ... în easybox Y"
	reReminderBody = regexp.MustCompile(`(?i)coletul\s+eMAG(?:\s+Marketplace[^,]*,\s*eMAG)?\s+num[ăa]rul\s+(\d+)\s+te\s+mai\s+a[șs]teapt[ăa]\s+p[âa]n[ăa][^\n\r<]+?[îi]n\s+easybox\s+([^,\n\r<]+)`)

	reQty      = regexp.MustCompile(`(\d+)\s*buc`)
	rePriceLei = regexp.MustCompile(`(?i)(-?\d{1,3}(?:[\.\s]\d{3})*(?:,\d{1,2})?)\s*LEI`)
	reTotalLei = regexp.MustCompile(`(?i)Total\s*:?\s*(-?\d{1,3}(?:[\.\s]\d{3})*(?:,\d{1,2})?)\s*LEI`)
	reDeadline = regexp.MustCompile(`(?i)p[âa]n[ăa]\s+([A-Za-zÎÂȘȚăâîșț]+),?\s+(\d{1,2})\s+([A-Za-zăâîșț]+)\.?\s+ora\s+(\d{1,2}):(\d{2})`)
	reQRURL    = regexp.MustCompile(`(?i)(https?://[^"'\s]*?/qr-image/([A-Z0-9]+))`)
	reQRImg    = regexp.MustCompile(`(?i)<img[^>]+src="(https?://[^"]*?qr[^"]*)"`)
)

var roMonths = map[string]time.Month{
	"ian": time.January, "ianuarie": time.January,
	"feb": time.February, "februarie": time.February,
	"mar": time.March, "martie": time.March,
	"apr": time.April, "aprilie": time.April,
	"mai": time.May,
	"iun": time.June, "iunie": time.June,
	"iul": time.July, "iulie": time.July,
	"aug": time.August, "august": time.August,
	"sep": time.September, "septembrie": time.September,
	"oct": time.October, "octombrie": time.October,
	"noi": time.November, "noiembrie": time.November,
	"dec": time.December, "decembrie": time.December,
}

// ClassifyEmail decides the email type. Body is plain-text or HTML-stripped text.
func ClassifyEmail(subject, textBody string) string {
	if reConfirmationSubject.MatchString(subject) {
		return "confirmation"
	}
	if reShippedSubject.MatchString(subject) {
		return "shipped"
	}
	// Sameday reminder ("te mai așteaptă") — kept for both eMAG and Marketplace,
	// because it means the package is in the easybox waiting to expire.
	if reReminderBody.MatchString(textBody) {
		return "arrived"
	}
	// Arrival ("a ajuns") — only for eMAG-direct, marketplace arrivals skipped.
	if reMarketplaceArrived.MatchString(textBody) {
		return ""
	}
	if reArrivedBody.MatchString(textBody) {
		return "arrived"
	}
	return ""
}

// ParseConfirmation handles "Confirmare înregistrare comandă #NNN".
// Keeps products in sections whose banner contains "livrate de eMAG".
func ParseConfirmation(subject, htmlBody string) (*ParsedEmail, error) {
	m := reConfirmationSubject.FindStringSubmatch(subject)
	if m == nil {
		return nil, fmt.Errorf("confirmation: no order number")
	}
	pe := &ParsedEmail{Kind: "confirmation", OrderNumber: m[1]}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(htmlBody))
	if err != nil {
		return nil, err
	}
	extractEmagSections(doc, pe)
	return pe, nil
}

// ParseShipped handles "Comanda ta #NNN a fost predată curierului".
// All products listed here are eMAG-delivered by definition.
func ParseShipped(subject, htmlBody string) (*ParsedEmail, error) {
	m := reShippedSubject.FindStringSubmatch(subject)
	if m == nil {
		return nil, fmt.Errorf("shipped: no order number")
	}
	pe := &ParsedEmail{Kind: "shipped", OrderNumber: m[1]}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(htmlBody))
	if err != nil {
		return nil, err
	}
	extractEmagSections(doc, pe)
	return pe, nil
}

// ParseArrived handles both Sameday variants:
//   - arrival: "Comanda ta eMAG numărul X a ajuns în easybox Y"
//   - reminder: "coletul eMAG ... numărul X te mai așteaptă până ... în easybox Y"
//
// Marketplace arrivals are rejected; marketplace reminders are kept (the user
// still needs to pick the package up before it expires).
func ParseArrived(htmlBody, textBody string) (*ParsedEmail, error) {
	var orderNum, easyboxFromBody string
	if m := reReminderBody.FindStringSubmatch(textBody); m != nil {
		orderNum = m[1]
		easyboxFromBody = strings.TrimSpace(m[2])
	} else {
		if reMarketplaceArrived.MatchString(textBody) {
			return nil, fmt.Errorf("arrived: marketplace (skip)")
		}
		m := reArrivedBody.FindStringSubmatch(textBody)
		if m == nil {
			return nil, fmt.Errorf("arrived: no order number")
		}
		orderNum = m[1]
		easyboxFromBody = strings.TrimSpace(strings.TrimSuffix(m[2], "."))
	}
	pe := &ParsedEmail{Kind: "arrived", OrderNumber: orderNum}

	// Deadline
	if dm := reDeadline.FindStringSubmatch(textBody); dm != nil {
		day, _ := strconv.Atoi(dm[2])
		hour, _ := strconv.Atoi(dm[4])
		min, _ := strconv.Atoi(dm[5])
		if mo, ok := roMonths[strings.ToLower(strings.TrimSuffix(dm[3], "."))]; ok {
			loc, _ := time.LoadLocation("Europe/Bucharest")
			if loc == nil {
				loc = time.UTC
			}
			now := time.Now().In(loc)
			t := time.Date(now.Year(), mo, day, hour, min, 0, 0, loc)
			if t.Before(now.Add(-24 * time.Hour)) {
				t = t.AddDate(1, 0, 0)
			}
			pe.PickupDeadline = &t
		}
	}

	// QR URL — look for <img src="...qr-image/PIN..."> or generic "...qr..."
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(htmlBody))
	if err == nil {
		doc.Find("img").EachWithBreak(func(_ int, img *goquery.Selection) bool {
			src, ok := img.Attr("src")
			if !ok || src == "" {
				return true
			}
			lower := strings.ToLower(src)
			if strings.Contains(lower, "/qr-image/") || strings.Contains(lower, "qr-code") || strings.Contains(lower, "qrcode") {
				pe.QRURL = src
				return false
			}
			return true
		})
	}
	if pe.QRURL == "" {
		// Fallback regex on raw html.
		if m := reQRURL.FindStringSubmatch(htmlBody); m != nil {
			pe.QRURL = m[1]
		} else if m := reQRImg.FindStringSubmatch(htmlBody); m != nil {
			pe.QRURL = m[1]
		}
	}

	// PIN: prefer extracting from the QR URL (e.g. ".../qr-image/RP6JED9")
	if pe.QRURL != "" {
		if m := reQRURL.FindStringSubmatch(pe.QRURL); m != nil {
			pe.PinCode = strings.ToUpper(m[2])
		}
	}
	// Fallback: scan HTML for the stylized PIN table (7 boxes with single chars)
	if pe.PinCode == "" && doc != nil {
		pe.PinCode = extractStylizedPin(doc)
	}

	// Easybox name + address: take the <p> next to the <img alt="pin_easybox">
	// (only present in arrival emails; reminders only mention easybox name inline).
	if doc != nil {
		name, addr := extractEasyboxFromHTML(doc)
		pe.EasyboxName = name
		pe.EasyboxAddress = addr
	}
	if pe.EasyboxName == "" {
		pe.EasyboxName = easyboxFromBody
	}

	return pe, nil
}

// ---------- helpers ----------

// extractEmagSections walks <tr> rows in document order, tracks whether
// the current section (delimited by "Produse ..." banners) is eMAG-delivered,
// and collects products + the last "Total:" value seen inside an eligible section.
func extractEmagSections(doc *goquery.Document, pe *ParsedEmail) {
	eligible := false
	seen := map[string]bool{}
	var currentGroupLabel string

	doc.Find("tr").Each(func(_ int, tr *goquery.Selection) {
		if banner, label := matchBanner(tr); banner {
			eligible = isEmagDelivered(label)
			currentGroupLabel = label
			return
		}
		if !eligible {
			return
		}

		text := strings.TrimSpace(tr.Text())
		if text == "" {
			return
		}

		// Capture total-ish rows (only when eligible)
		if tm := reTotalLei.FindStringSubmatch(text); tm != nil && isTotalRow(text) {
			v := priceToBani(tm[1])
			if v > 0 {
				pe.TotalBani = v
			}
		}

		if p, ok := extractProductRow(tr); ok {
			if seen[p.Name] {
				return
			}
			seen[p.Name] = true
			if p.SellerGroup == "" {
				p.SellerGroup = sellerFromBanner(currentGroupLabel)
			}
			pe.Products = append(pe.Products, p)
		}
	})
}

func matchBanner(tr *goquery.Selection) (ok bool, text string) {
	tds := tr.ChildrenFiltered("td")
	if tds.Length() != 1 {
		return false, ""
	}
	t := strings.Join(strings.Fields(tds.Text()), " ")
	if len(t) == 0 || len(t) > 150 {
		return false, ""
	}
	lower := strings.ToLower(t)
	if !strings.HasPrefix(lower, "produse livrate") && !strings.HasPrefix(lower, "produse vândute") && !strings.HasPrefix(lower, "produse vandute") {
		return false, ""
	}
	return true, t
}

func isEmagDelivered(banner string) bool {
	l := strings.ToLower(banner)
	l = strings.ReplaceAll(l, "ă", "a")
	l = strings.ReplaceAll(l, "â", "a")
	l = strings.ReplaceAll(l, "î", "i")
	l = strings.ReplaceAll(l, "ș", "s")
	l = strings.ReplaceAll(l, "ț", "t")
	// Accept any banner that says something is delivered BY eMAG
	//   "produse livrate de emag"
	//   "produse vandute si livrate de emag"
	//   "produse vandute de X si livrate de emag"
	//   "produse vandute de emag" (sub-banner inside an emag-delivered outer group)
	if strings.Contains(l, "livrate de emag") {
		return true
	}
	if strings.Contains(l, "vandute de emag") {
		return true
	}
	return false
}

func sellerFromBanner(banner string) string {
	l := strings.ToLower(banner)
	l = strings.ReplaceAll(l, "ă", "a")
	l = strings.ReplaceAll(l, "â", "a")
	l = strings.ReplaceAll(l, "î", "i")
	l = strings.ReplaceAll(l, "ș", "s")
	l = strings.ReplaceAll(l, "ț", "t")
	if strings.Contains(l, "vandute de emag") {
		return "eMAG"
	}
	// "Produse vândute de X și livrate de eMAG" → X
	reSeller := regexp.MustCompile(`(?i)vandute de (.+?)\s+si livrate de emag`)
	if m := reSeller.FindStringSubmatch(l); m != nil {
		return strings.TrimSpace(m[1])
	}
	return "eMAG"
}

// isTotalRow returns true if the row is a "Total:" line (not a product row).
func isTotalRow(text string) bool {
	// Heuristic: a total row has "Total:" prefix (after whitespace) and doesn't have "N buc".
	l := strings.ToLower(strings.TrimSpace(text))
	if !strings.HasPrefix(l, "total") {
		// Sometimes there is leading whitespace from sibling cells
		idx := strings.Index(l, "total:")
		if idx < 0 {
			return false
		}
	}
	return !reQty.MatchString(text)
}

// extractProductRow returns a Product when the row looks like a product row.
// Product row signals:
//   - contains an <img> (preferably from s13emagst.akamaized.net)
//   - has an <a title="..."> for the product name
//   - has "N buc" and "X Lei" in its text
func extractProductRow(tr *goquery.Selection) (Product, bool) {
	text := strings.Join(strings.Fields(tr.Text()), " ")
	if text == "" {
		return Product{}, false
	}
	qtyMatch := reQty.FindStringSubmatch(text)
	if qtyMatch == nil {
		return Product{}, false
	}
	priceMatches := rePriceLei.FindAllStringSubmatch(text, -1)
	if len(priceMatches) == 0 {
		return Product{}, false
	}

	// Reject rows that are discount/shipping/total lines
	lo := strings.ToLower(text)
	if strings.Contains(lo, "discount") || strings.Contains(lo, "reducere") ||
		strings.Contains(lo, "cost livrare") || strings.Contains(lo, "servicii") ||
		strings.HasPrefix(strings.TrimSpace(lo), "total") {
		return Product{}, false
	}

	// Image
	var imgURL string
	if img := tr.Find("img").First(); img.Length() > 0 {
		imgURL, _ = img.Attr("src")
	}
	// A product row should have a product image from eMAG's product CDN.
	if imgURL == "" || !strings.Contains(imgURL, "emagst.akamaized.net") {
		return Product{}, false
	}

	// Name: prefer <a title="..."> on a link.
	name := ""
	tr.Find("a[title]").EachWithBreak(func(_ int, a *goquery.Selection) bool {
		if t, ok := a.Attr("title"); ok && len(strings.TrimSpace(t)) > 3 {
			name = strings.TrimSpace(t)
			return false
		}
		return true
	})
	if name == "" {
		if a := tr.Find("a").First(); a.Length() > 0 {
			name = strings.TrimSpace(a.Text())
		}
	}
	if name == "" {
		return Product{}, false
	}

	qty, _ := strconv.Atoi(qtyMatch[1])

	// Line total = last price (typical eMAG layout puts the positive line total at the right).
	var bani int64
	for i := len(priceMatches) - 1; i >= 0; i-- {
		v := priceToBani(priceMatches[i][1])
		if v > 0 {
			bani = v
			break
		}
	}

	return Product{
		Name:          name,
		ImageURL:      imgURL,
		Qty:           qty,
		LineTotalBani: bani,
	}, true
}

func priceToBani(s string) int64 {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, ".", "")
	s = strings.ReplaceAll(s, ",", ".")
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return int64(f * 100)
}

// extractStylizedPin finds the 7-box PIN rendered as individual styled <span>
// elements in the Sameday arrived email (used only as fallback if QR URL parse fails).
func extractStylizedPin(doc *goquery.Document) string {
	var best string
	doc.Find("table").EachWithBreak(func(_ int, tbl *goquery.Selection) bool {
		chars := []byte{}
		tbl.Find("span").Each(func(_ int, sp *goquery.Selection) {
			t := strings.TrimSpace(sp.Text())
			if len(t) == 1 {
				r := t[0]
				if (r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') {
					chars = append(chars, r)
				}
			}
		})
		if len(chars) >= 6 && len(chars) <= 10 {
			best = string(chars)
			return false
		}
		return true
	})
	return strings.ToUpper(best)
}

// extractEasyboxFromHTML finds the block next to the pin_easybox icon and
// returns the first two meaningful lines (name, address).
func extractEasyboxFromHTML(doc *goquery.Document) (name, addr string) {
	// The pin icon is <img alt="pin_easybox">. Find the nearest <p> after it
	// in the enclosing row or parent block.
	var captured []string
	doc.Find("img[alt='pin_easybox']").EachWithBreak(func(_ int, img *goquery.Selection) bool {
		// Walk up to the enclosing row/table and search for a <p>
		container := img.Parent().Parent() // img -> td -> tr
		var text string
		if p := container.Find("p").First(); p.Length() > 0 {
			text, _ = p.Html()
		} else if pp := img.Closest("table").Find("p").First(); pp.Length() > 0 {
			text, _ = pp.Html()
		}
		if text != "" {
			// Replace <br> with newlines then strip tags
			text = strings.ReplaceAll(text, "<br>", "\n")
			text = strings.ReplaceAll(text, "<br/>", "\n")
			text = strings.ReplaceAll(text, "<br />", "\n")
			text = stripTags(text)
			for _, l := range strings.Split(text, "\n") {
				l = strings.TrimSpace(l)
				if l == "" {
					continue
				}
				lo := strings.ToLower(l)
				if strings.HasPrefix(lo, "program easybox") {
					continue
				}
				captured = append(captured, l)
				if len(captured) >= 2 {
					break
				}
			}
			return false
		}
		return true
	})
	if len(captured) >= 1 {
		name = captured[0]
	}
	if len(captured) >= 2 {
		addr = captured[1]
	}
	return
}

// stripTags is a tiny best-effort HTML tag stripper for short fragments.
func stripTags(s string) string {
	var b strings.Builder
	inTag := false
	for _, r := range s {
		switch r {
		case '<':
			inTag = true
		case '>':
			inTag = false
		default:
			if !inTag {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

// htmlToText returns a plain-text rendering of an HTML body suitable for
// regex-based classification of arrived emails. Preserves newlines crudely.
func htmlToText(h string) string {
	var b strings.Builder
	inTag := false
	for _, r := range h {
		switch r {
		case '<':
			inTag = true
		case '>':
			inTag = false
			b.WriteRune('\n')
		default:
			if !inTag {
				b.WriteRune(r)
			}
		}
	}
	s := b.String()
	// HTML entity decoding for the handful we care about
	repls := []string{
		"&nbsp;", " ",
		"&amp;", "&",
		"&lt;", "<",
		"&gt;", ">",
		"&#x103;", "ă",
		"&#x219;", "ș",
		"&#x21B;", "ț",
		"&#xEE;", "î",
		"&#xE2;", "â",
		"&#xCE;", "Î",
		"&#xC2;", "Â",
		"&#x15F;", "ș",
		"&#x163;", "ț",
	}
	r := strings.NewReplacer(repls...)
	s = r.Replace(s)
	return s
}
