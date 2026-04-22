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
	QRContentID    string // cid referenced in the HTML near PIN block
	QRAttachmentID string // filled later by proton fetch layer
}

var (
	reConfirmationSubject = regexp.MustCompile(`(?i)Confirmare\s+(?:înregistrare|inregistrare)\s+comand[ăa]\s*#?\s*(\d+)`)
	reShippedSubject      = regexp.MustCompile(`(?i)Comanda\s+ta\s*#?\s*(\d+)\s+a\s+fost\s+predat[ăa]\s+curierului`)
	reArrivedBody         = regexp.MustCompile(`(?i)Comanda\s+ta\s+eMAG\s+num[ăa]rul\s+(\d+)\s+a\s+ajuns\s+[îi]n\s+([^\n\r]+)`)
	reMarketplaceArrived  = regexp.MustCompile(`(?i)Comanda\s+ta\s+eMAG\s+Marketplace\s*-[^\n]*,\s*eMAG\s+num[ăa]rul`)
	rePriceLei            = regexp.MustCompile(`(-?\d{1,3}(?:[\.\s]\d{3})*(?:,\d{1,2})?)\s*LEI`)
	reQty                 = regexp.MustCompile(`(\d+)\s*buc`)
	reDeadline            = regexp.MustCompile(`(?i)p[âa]n[ăa]\s+([A-Za-zÎÂȘȚăâîșț]+),?\s+(\d{1,2})\s+([A-Za-zăâîșț]+)\.?\s+ora\s+(\d{1,2}):(\d{2})`)
	rePinLabel            = regexp.MustCompile(`(?i)Sau\s+tasteaz[ăa]\s+pe\s+ecranul\s+easybox\s+codul\s*:`)
	rePastrareLabel       = regexp.MustCompile(`(?i)p[ăa]strare\.`)
	reCIDSrc              = regexp.MustCompile(`(?i)cid:([^"'\s>]+)`)
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

// ClassifyEmail returns the kind of interesting email or "" if it's not one we track.
func ClassifyEmail(subject, plainBody string) string {
	if m := reConfirmationSubject.FindString(subject); m != "" {
		return "confirmation"
	}
	if m := reShippedSubject.FindString(subject); m != "" {
		return "shipped"
	}
	// Arrived: body-based. Reject marketplace arrivals first.
	if reMarketplaceArrived.MatchString(plainBody) {
		return ""
	}
	if reArrivedBody.MatchString(plainBody) {
		return "arrived"
	}
	return ""
}

// ParseConfirmation handles "Confirmare înregistrare comandă #NNN" emails.
// Extracts products from groups that are delivered by eMAG (both
// "Produse vândute de eMAG" and "Produse vândute de X și livrate de eMAG").
// Ignores groups "Produse vândute și livrate de <non-eMAG>".
func ParseConfirmation(subject string, htmlBody string) (*ParsedEmail, error) {
	num := extractOrderNumber(subject, reConfirmationSubject)
	if num == "" {
		return nil, fmt.Errorf("confirmation: no order number in subject")
	}
	pe := &ParsedEmail{Kind: "confirmation", OrderNumber: num}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(htmlBody))
	if err != nil {
		return nil, err
	}
	extractEmagDeliveredGroups(doc, pe, true)
	return pe, nil
}

// ParseShipped handles "Comanda ta #NNN a fost predată curierului" emails.
// Here all groups listed are eMAG-delivered, so we keep every product.
func ParseShipped(subject string, htmlBody string) (*ParsedEmail, error) {
	num := extractOrderNumber(subject, reShippedSubject)
	if num == "" {
		return nil, fmt.Errorf("shipped: no order number in subject")
	}
	pe := &ParsedEmail{Kind: "shipped", OrderNumber: num}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(htmlBody))
	if err != nil {
		return nil, err
	}
	extractEmagDeliveredGroups(doc, pe, false)
	return pe, nil
}

// ParseArrived handles "Comanda ta eMAG numărul NNN a ajuns în easybox X" emails.
// Extracts PIN, deadline, easybox name+address and QR content-id.
func ParseArrived(htmlBody string, plainBody string) (*ParsedEmail, error) {
	if reMarketplaceArrived.MatchString(plainBody) {
		return nil, fmt.Errorf("arrived: marketplace, not eMAG-delivered")
	}
	match := reArrivedBody.FindStringSubmatch(plainBody)
	if match == nil {
		return nil, fmt.Errorf("arrived: no order number in body")
	}
	pe := &ParsedEmail{Kind: "arrived", OrderNumber: match[1], EasyboxName: strings.TrimSpace(strings.TrimSuffix(match[2], "."))}

	// Deadline
	if dm := reDeadline.FindStringSubmatch(plainBody); dm != nil {
		day, _ := strconv.Atoi(dm[2])
		hour, _ := strconv.Atoi(dm[4])
		min, _ := strconv.Atoi(dm[5])
		if mo, ok := roMonths[strings.ToLower(strings.TrimSuffix(dm[3], "."))]; ok {
			now := time.Now()
			loc, _ := time.LoadLocation("Europe/Bucharest")
			if loc == nil {
				loc = time.UTC
			}
			t := time.Date(now.Year(), mo, day, hour, min, 0, 0, loc)
			if t.Before(now.Add(-24 * time.Hour)) {
				t = t.AddDate(1, 0, 0)
			}
			pe.PickupDeadline = &t
		}
	}

	// PIN: after "Sau tastează pe ecranul easybox codul:" collect next lines' first chars
	pe.PinCode = extractPin(plainBody)

	// Easybox name + address: first two non-empty lines after "păstrare."
	name, addr := extractEasyboxInfo(plainBody)
	if name != "" {
		pe.EasyboxName = name
	}
	pe.EasyboxAddress = addr

	// QR cid: find an <img src="cid:..."> near the PIN / pin_easybox reference
	pe.QRContentID = extractQRContentID(htmlBody)

	return pe, nil
}

func extractOrderNumber(s string, re *regexp.Regexp) string {
	m := re.FindStringSubmatch(s)
	if m == nil || len(m) < 2 {
		return ""
	}
	return m[1]
}

func extractEmagDeliveredGroups(doc *goquery.Document, pe *ParsedEmail, requireLivrateDeEmag bool) {
	// Find every heading/cell that announces a group, then walk forward for product rows.
	text := doc.Text()
	// Collect order total from last "Total:" near end of email (best-effort)
	pe.TotalBani = extractLastTotal(text)

	// Walk all elements; when we hit a header-like text, decide if the group is eMAG-delivered.
	// Strategy: iterate over the text nodes by walking the DOM in order.
	var groups []groupMatch
	doc.Find("*").Each(func(_ int, sel *goquery.Selection) {
		t := strings.TrimSpace(sel.Text())
		if t == "" {
			return
		}
		if isGroupHeader(t) {
			groups = append(groups, groupMatch{header: t, node: sel})
		}
	})
	// Deduplicate overlapping groups: keep only the deepest unique header text-to-node association.
	seen := map[string]bool{}
	for _, g := range groups {
		key := normHeader(g.header)
		if seen[key] {
			continue
		}
		seen[key] = true

		emagDelivered := headerIsEmagDelivered(g.header)
		if requireLivrateDeEmag && !emagDelivered {
			continue
		}
		if !requireLivrateDeEmag && !emagDelivered {
			// For shipped emails, still require "livrate de eMAG" to avoid false positives.
			continue
		}
		prods := extractProductsAfter(g.node, g.header)
		for i := range prods {
			prods[i].SellerGroup = groupLabel(g.header)
		}
		pe.Products = append(pe.Products, prods...)
	}
}

type groupMatch struct {
	header string
	node   *goquery.Selection
}

func normHeader(h string) string {
	return strings.Join(strings.Fields(strings.ToLower(h)), " ")
}

func isGroupHeader(t string) bool {
	lower := strings.ToLower(t)
	// Exact-ish header match (not a paragraph that merely contains the phrase)
	if len(t) > 200 {
		return false
	}
	return strings.Contains(lower, "produse vândute") || strings.Contains(lower, "produse vandute")
}

func headerIsEmagDelivered(h string) bool {
	lower := strings.ToLower(h)
	lower = strings.ReplaceAll(lower, "ă", "a")
	lower = strings.ReplaceAll(lower, "â", "a")
	lower = strings.ReplaceAll(lower, "î", "i")
	lower = strings.ReplaceAll(lower, "ș", "s")
	lower = strings.ReplaceAll(lower, "ț", "t")
	// Accepted patterns:
	//   "produse vandute de emag"
	//   "produse vandute de X si livrate de emag"
	// Rejected:
	//   "produse vandute si livrate de <non-emag>"
	//   "produse vandute si livrate de emag" (also accepted — delivered by eMAG)
	if strings.Contains(lower, "vandute de emag") {
		return true
	}
	if strings.Contains(lower, "livrate de emag") {
		return true
	}
	return false
}

func groupLabel(h string) string {
	l := strings.ToLower(h)
	if strings.Contains(l, "vândute de emag") || strings.Contains(l, "vandute de emag") {
		return "eMAG"
	}
	return strings.TrimSpace(h)
}

// extractProductsAfter finds product rows after the given group header element.
// It looks for subsequent elements that contain an <img>, a product name, a "X buc" pattern,
// and a "<price> Lei" line — until it hits another group header or the end.
func extractProductsAfter(node *goquery.Selection, header string) []Product {
	var out []Product
	// Walk forward through following siblings and their descendants.
	// Use a flat walker: from the node, go up until we find an ancestor that has "following" elements.
	container := node
	// Look for table rows as siblings
	var candidates []*goquery.Selection
	container.NextAll().Each(func(_ int, s *goquery.Selection) {
		candidates = append(candidates, s)
		s.Find("*").Each(func(_ int, inner *goquery.Selection) {
			candidates = append(candidates, inner)
		})
	})
	// Also scan parent's siblings (common pattern: header is a <td>/<div>, product rows are sibling <tr>/<div>s)
	container.Parent().NextAll().Each(func(_ int, s *goquery.Selection) {
		candidates = append(candidates, s)
	})

	seenNames := map[string]bool{}
	for _, c := range candidates {
		t := strings.TrimSpace(c.Text())
		if t == "" {
			continue
		}
		if isGroupHeader(t) && normHeader(t) != normHeader(header) {
			break
		}
		// Heuristic: row contains qty "X buc" and a price "Y Lei"
		qtyMatch := reQty.FindStringSubmatch(t)
		priceMatches := rePriceLei.FindAllStringSubmatch(t, -1)
		if qtyMatch == nil || len(priceMatches) == 0 {
			continue
		}
		// Skip rows that look like discount/shipping ("Discount conform", "Cost livrare", "Total:")
		lo := strings.ToLower(t)
		if strings.Contains(lo, "discount") || strings.Contains(lo, "reducere") ||
			strings.Contains(lo, "cost livrare") || strings.HasPrefix(strings.TrimSpace(lo), "total") {
			continue
		}
		qty, _ := strconv.Atoi(qtyMatch[1])
		// Product line total is usually the LAST non-negative price on the row.
		var bani int64
		for i := len(priceMatches) - 1; i >= 0; i-- {
			v := priceToBani(priceMatches[i][1])
			if v >= 0 {
				bani = v
				break
			}
		}
		name := extractProductName(c, t)
		if name == "" {
			continue
		}
		if seenNames[name] {
			continue
		}
		seenNames[name] = true
		imgURL := ""
		if img := c.Find("img").First(); img.Length() > 0 {
			imgURL, _ = img.Attr("src")
		}
		out = append(out, Product{
			Name:          name,
			ImageURL:      imgURL,
			Qty:           qty,
			LineTotalBani: bani,
		})
	}
	return out
}

func extractProductName(row *goquery.Selection, rowText string) string {
	// Prefer an <a> text or an <img alt> if present.
	if a := row.Find("a").First(); a.Length() > 0 {
		t := strings.TrimSpace(a.Text())
		if len(t) > 3 {
			return t
		}
	}
	if img := row.Find("img").First(); img.Length() > 0 {
		if alt, ok := img.Attr("alt"); ok && len(alt) > 3 {
			return alt
		}
	}
	// Fallback: the part of rowText before "\t" or "N buc"
	t := strings.ReplaceAll(rowText, "\n", " ")
	t = strings.Join(strings.Fields(t), " ")
	if idx := reQty.FindStringIndex(t); idx != nil {
		return strings.TrimSpace(t[:idx[0]])
	}
	return ""
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

func extractLastTotal(text string) int64 {
	// Find the LAST occurrence of "Total:" followed by a price.
	reTotal := regexp.MustCompile(`(?i)Total\s*:\s*(-?\d{1,3}(?:[\.\s]\d{3})*(?:,\d{1,2})?)\s*LEI`)
	matches := reTotal.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return 0
	}
	return priceToBani(matches[len(matches)-1][1])
}

func extractPin(plainBody string) string {
	loc := rePinLabel.FindStringIndex(plainBody)
	if loc == nil {
		return ""
	}
	after := plainBody[loc[1]:]
	lines := strings.Split(after, "\n")
	var chars []byte
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		// If the line has any non-space content that doesn't look like a single char PIN digit,
		// but the first char is a letter/digit, use it.
		r := l[0]
		if (r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
			chars = append(chars, r)
		}
		if len(chars) >= 7 {
			break
		}
	}
	return strings.ToUpper(string(chars))
}

func extractEasyboxInfo(plainBody string) (name, addr string) {
	loc := rePastrareLabel.FindStringIndex(plainBody)
	if loc == nil {
		return "", ""
	}
	after := plainBody[loc[1]:]
	lines := strings.Split(after, "\n")
	var collected []string
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		// Skip placeholder tokens / image alts
		lo := strings.ToLower(l)
		if lo == "pin_easybox" || lo == "qr_easybox" || strings.HasPrefix(lo, "program easybox") {
			continue
		}
		collected = append(collected, l)
		if len(collected) >= 2 {
			break
		}
	}
	if len(collected) >= 1 {
		name = collected[0]
	}
	if len(collected) >= 2 {
		addr = collected[1]
	}
	return
}

func extractQRContentID(htmlBody string) string {
	// Pick an <img src="cid:..."> with a qr-ish context; if multiple, pick the one whose alt/filename hints QR.
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(htmlBody))
	if err != nil {
		return ""
	}
	var best string
	var fallback string
	doc.Find("img").Each(func(_ int, img *goquery.Selection) {
		src, _ := img.Attr("src")
		m := reCIDSrc.FindStringSubmatch(src)
		if m == nil {
			return
		}
		cid := m[1]
		alt, _ := img.Attr("alt")
		srcLower := strings.ToLower(src + " " + alt)
		if strings.Contains(srcLower, "qr") {
			if best == "" {
				best = cid
			}
		}
		if fallback == "" {
			fallback = cid
		}
	})
	if best != "" {
		return best
	}
	return fallback
}
