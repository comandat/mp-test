package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
)

// ParsedEmail is the structured form of any order email we understand.
//
// Confirmation and shipped emails populate Shipments (one per delivery group)
// together with OrderNumber and TotalBani. Arrived emails populate the
// arrival-only fields (ArrivalEasybox, PinCode, QRURL, PickupDeadline); the
// store then matches the arrival to a specific shipment by easybox name.
type ParsedEmail struct {
	Kind        string // confirmation | shipped | arrived
	OrderNumber string
	TotalBani   int64
	Shipments   []ParsedShipment

	// Arrival-only fields.
	ArrivalEasybox     string
	ArrivalEasyboxAddr string
	PickupDeadline     *time.Time
	PinCode            string
	QRURL              string
}

// ParsedShipment is a single delivery group from a confirmation/shipped email.
// It carries its own products, total, easybox name and seller/courier labels.
type ParsedShipment struct {
	GroupIndex      int
	DeliveryBy      string // who ships (eMAG / seller name)
	DeliveredByEmag bool
	SellerGroup     string // who sold the items (often same as DeliveryBy)
	EasyboxName     string
	Products        []Product
	TotalBani       int64
}

var (
	reConfirmationSubject = regexp.MustCompile(`(?i)Confirmare\s+(?:înregistrare|inregistrare)\s+comand[ăa]\s*#?\s*(\d+)`)
	reShippedSubject      = regexp.MustCompile(`(?i)Comanda\s+ta\s*#?\s*(\d+)\s+a\s+fost\s+predat[ăa]\s+curierului`)
	reArrivedBody         = regexp.MustCompile(`(?i)Comanda\s+ta\s+eMAG\s+num[ăa]rul\s+(\d+)\s+a\s+ajuns\s+[îi]n\s+([^.\n\r<]+)`)
	reMarketplaceArrived  = regexp.MustCompile(`(?i)Comanda\s+ta\s+eMAG\s+Marketplace\s*-[^\n]*,\s*eMAG\s+num[ăa]rul`)
	reReminderBody        = regexp.MustCompile(`(?i)coletul\s+eMAG(?:\s+Marketplace[^,]*,\s*eMAG)?\s+num[ăa]rul\s+(\d+)\s+te\s+mai\s+a[șs]teapt[ăa]\s+p[âa]n[ăa][^\n\r<]+?[îi]n\s+easybox\s+([^,\n\r<]+)`)

	reQty      = regexp.MustCompile(`(\d+)\s*buc`)
	rePriceLei = regexp.MustCompile(`(?i)(-?\d{1,3}(?:[\.\s]\d{3})*(?:,\d{1,2})?)\s*LEI`)
	reTotalLei = regexp.MustCompile(`(?i)Total\s*:?\s*(-?\d{1,3}(?:[\.\s]\d{3})*(?:,\d{1,2})?)\s*LEI`)
	reDeadline = regexp.MustCompile(`(?i)p[âa]n[ăa]\s+([A-Za-zÎÂȘȚăâîșț]+),?\s+(\d{1,2})\s+([A-Za-zăâîșț]+)\.?\s+ora\s+(\d{1,2}):(\d{2})`)
	reQRURL    = regexp.MustCompile(`(?i)(https?://[^"'\s]*?/qr-image/([A-Z0-9]+))`)
	reQRImg    = regexp.MustCompile(`(?i)<img[^>]+src="(https?://[^"]*?qr[^"]*)"`)
	reEasybox  = regexp.MustCompile(`(?i)Livrare\s+[îi]n\s+easybox\s+(.+?)\s*$`)
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

// ClassifyEmail decides the email type from subject + body text.
func ClassifyEmail(subject, textBody string) string {
	if reConfirmationSubject.MatchString(subject) {
		return "confirmation"
	}
	if reShippedSubject.MatchString(subject) {
		return "shipped"
	}
	if reReminderBody.MatchString(textBody) {
		return "arrived"
	}
	if reMarketplaceArrived.MatchString(textBody) {
		return ""
	}
	if reArrivedBody.MatchString(textBody) {
		return "arrived"
	}
	return ""
}

// ParseConfirmation handles "Confirmare înregistrare comandă #NNN".
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
	pe.Shipments = extractShipments(doc)
	pe.TotalBani = sumShipmentTotals(pe.Shipments)
	return pe, nil
}

// ParseShipped handles "Comanda ta #NNN a fost predată curierului". Shape is
// the same as confirmation emails, so the same extractor is reused.
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
	pe.Shipments = extractShipments(doc)
	pe.TotalBani = sumShipmentTotals(pe.Shipments)
	return pe, nil
}

// ParseArrived handles both Sameday variants (arrival + reminder). It fills
// OrderNumber + ArrivalEasybox + PIN/QR/deadline; shipment matching happens
// in the store.
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
		if m := reQRURL.FindStringSubmatch(htmlBody); m != nil {
			pe.QRURL = m[1]
		} else if m := reQRImg.FindStringSubmatch(htmlBody); m != nil {
			pe.QRURL = m[1]
		}
	}

	if pe.QRURL != "" {
		if m := reQRURL.FindStringSubmatch(pe.QRURL); m != nil {
			pe.PinCode = strings.ToUpper(m[2])
		}
	}
	if pe.PinCode == "" && doc != nil {
		pe.PinCode = extractStylizedPin(doc)
	}

	if doc != nil {
		name, addr := extractEasyboxFromHTML(doc)
		pe.ArrivalEasybox = name
		pe.ArrivalEasyboxAddr = addr
	}
	if pe.ArrivalEasybox == "" {
		pe.ArrivalEasybox = easyboxFromBody
	}

	return pe, nil
}

// ---------- shipment extraction ----------

// extractShipments walks <tr> rows in document order and returns one
// ParsedShipment per delivery group. A new shipment starts whenever a
// "Produse livrate de X" or "Produse vândute și livrate de X" banner is
// encountered. Seller sub-headers ("Produse vândute de X" without "livrate")
// update the current shipment's seller label but never start a new group.
func extractShipments(doc *goquery.Document) []ParsedShipment {
	var shipments []ParsedShipment
	var cur *ParsedShipment
	seenProduct := map[string]bool{}

	finish := func() {
		if cur == nil {
			return
		}
		shipments = append(shipments, *cur)
		cur = nil
	}

	doc.Find("tr").Each(func(_ int, tr *goquery.Selection) {
		// Skip rows that wrap other rows (their .Text() would otherwise
		// concatenate all descendants and muddle easybox / Total detection).
		if tr.Find("tr").Length() > 0 {
			return
		}
		if banner, label := matchBanner(tr); banner {
			if isDeliveryBanner(label) {
				finish()
				cur = &ParsedShipment{
					GroupIndex:      len(shipments),
					DeliveryBy:      deliveryPartyFromBanner(label),
					DeliveredByEmag: isEmagDelivered(label),
					SellerGroup:     sellerFromBanner(label),
				}
			} else if cur != nil {
				// Seller sub-header inside the current shipment.
				if seller := sellerFromVanduteBanner(label); seller != "" {
					cur.SellerGroup = seller
				}
			}
			return
		}
		if cur == nil {
			return
		}

		text := strings.TrimSpace(tr.Text())
		if text == "" {
			return
		}

		// "Livrare în easybox X"
		if cur.EasyboxName == "" {
			oneLine := strings.Join(strings.Fields(text), " ")
			if em := reEasybox.FindStringSubmatch(oneLine); em != nil {
				cur.EasyboxName = strings.TrimSpace(em[1])
				return
			}
		}

		// "Total: X Lei" — not a product row (we check that first)
		if tm := reTotalLei.FindStringSubmatch(text); tm != nil && isTotalRow(text) {
			if v := priceToBani(tm[1]); v > 0 {
				cur.TotalBani = v
			}
			return
		}

		if p, ok := extractProductRow(tr); ok {
			if seenProduct[p.Name] {
				return
			}
			seenProduct[p.Name] = true
			if p.SellerGroup == "" {
				p.SellerGroup = cur.SellerGroup
			}
			cur.Products = append(cur.Products, p)
		}
	})
	finish()
	return shipments
}

func sumShipmentTotals(shipments []ParsedShipment) int64 {
	var sum int64
	for _, s := range shipments {
		sum += s.TotalBani
	}
	return sum
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
	if !strings.HasPrefix(lower, "produse livrate") &&
		!strings.HasPrefix(lower, "produse vândute") &&
		!strings.HasPrefix(lower, "produse vandute") {
		return false, ""
	}
	return true, t
}

func isDeliveryBanner(banner string) bool {
	return strings.Contains(normalize(banner), "livrate")
}

func isEmagDelivered(banner string) bool {
	l := normalize(banner)
	if strings.Contains(l, "livrate de emag") {
		return true
	}
	if strings.Contains(l, "vandute de emag") {
		return true
	}
	return false
}

// deliveryPartyFromBanner returns who delivers the shipment, e.g. "eMAG" or
// "ONLINE TRADING".
func deliveryPartyFromBanner(banner string) string {
	l := normalize(banner)
	if strings.Contains(l, "livrate de emag") {
		return "eMAG"
	}
	// "Produse vândute și livrate de X" — capture X (preserve casing).
	re := regexp.MustCompile(`(?i)și\s+livrate\s+de\s+(.+?)\s*$|si\s+livrate\s+de\s+(.+?)\s*$|livrate\s+de\s+(.+?)\s*$`)
	if m := re.FindStringSubmatch(banner); m != nil {
		for _, s := range m[1:] {
			if s = strings.TrimSpace(s); s != "" {
				return s
			}
		}
	}
	return "eMAG"
}

// sellerFromBanner returns the seller for a delivery banner. For
// "Produse vândute și livrate de X" it is X. For "Produse livrate de eMAG"
// (outer group) the seller is initially eMAG and may be refined by a nested
// "Produse vândute de Y" sub-header.
func sellerFromBanner(banner string) string {
	l := normalize(banner)
	if strings.Contains(l, "vandute si livrate de") {
		re := regexp.MustCompile(`(?i)vândute\s+și\s+livrate\s+de\s+(.+?)\s*$|vandute\s+si\s+livrate\s+de\s+(.+?)\s*$`)
		if m := re.FindStringSubmatch(banner); m != nil {
			for _, s := range m[1:] {
				if s = strings.TrimSpace(s); s != "" {
					return s
				}
			}
		}
	}
	if strings.Contains(l, "livrate de emag") {
		return "eMAG"
	}
	return "eMAG"
}

// sellerFromVanduteBanner handles the nested "Produse vândute de X" sub-header
// (without any "livrate"). Returns X or empty if it can't parse.
func sellerFromVanduteBanner(banner string) string {
	re := regexp.MustCompile(`(?i)produse\s+vândute\s+de\s+(.+?)\s*$|produse\s+vandute\s+de\s+(.+?)\s*$`)
	if m := re.FindStringSubmatch(banner); m != nil {
		for _, s := range m[1:] {
			if s = strings.TrimSpace(s); s != "" {
				return s
			}
		}
	}
	return ""
}

func normalize(s string) string {
	l := strings.ToLower(s)
	l = strings.ReplaceAll(l, "ă", "a")
	l = strings.ReplaceAll(l, "â", "a")
	l = strings.ReplaceAll(l, "î", "i")
	l = strings.ReplaceAll(l, "ș", "s")
	l = strings.ReplaceAll(l, "ț", "t")
	return l
}

func isTotalRow(text string) bool {
	l := strings.ToLower(strings.TrimSpace(text))
	if !strings.HasPrefix(l, "total") {
		if idx := strings.Index(l, "total:"); idx < 0 {
			return false
		}
	}
	return !reQty.MatchString(text)
}

// extractProductRow returns a Product when the row looks like a product row.
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

	lo := strings.ToLower(text)
	if strings.Contains(lo, "discount") || strings.Contains(lo, "reducere") ||
		strings.Contains(lo, "cost livrare") || strings.Contains(lo, "servicii") ||
		strings.HasPrefix(strings.TrimSpace(lo), "total") {
		return Product{}, false
	}

	var imgURL string
	if img := tr.Find("img").First(); img.Length() > 0 {
		imgURL, _ = img.Attr("src")
	}
	if imgURL == "" || !strings.Contains(imgURL, "emagst.akamaized.net") {
		return Product{}, false
	}

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

	var bani int64
	for i := len(priceMatches) - 1; i >= 0; i-- {
		if v := priceToBani(priceMatches[i][1]); v > 0 {
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
// elements in the Sameday arrived email.
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

func extractEasyboxFromHTML(doc *goquery.Document) (name, addr string) {
	var captured []string
	doc.Find("img[alt='pin_easybox']").EachWithBreak(func(_ int, img *goquery.Selection) bool {
		container := img.Parent().Parent()
		var text string
		if p := container.Find("p").First(); p.Length() > 0 {
			text, _ = p.Html()
		} else if pp := img.Closest("table").Find("p").First(); pp.Length() > 0 {
			text, _ = pp.Html()
		}
		if text != "" {
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
