package main

import (
	"strings"
	"testing"
)

// Minimal HTML fragment mirroring the structure of the real "Confirmare înregistrare comandă" email.
const confirmationHTML = `<html><body>
<table>
<tr><td>Produse livrate de <a>eMAG</a></td></tr>
<tr><td><table>
  <tr><td>Livrare în easybox Casa Nasului</td></tr>
  <tr><td>Produse vândute de <a>eMAG</a></td></tr>
  <tr>
    <td><a><img src="https://s13emagst.akamaized.net/products/x/1.jpg" alt="img"></a></td>
    <td><a title="Supliment alimentar OstroVit Magnesium Glycinate 90 caps">Supliment...</a></td>
    <td>1&nbsp;buc</td>
    <td>42,40 Lei</td>
  </tr>
  <tr>
    <td><a><img src="https://s13emagst.akamaized.net/products/x/2.jpg" alt="img"></a></td>
    <td><a title="Solutie de curatat universala dezinfectant Sanytol Eucalipt, 500 ml">Solutie...</a></td>
    <td>1&nbsp;buc</td>
    <td>10,12 Lei</td>
  </tr>
  <tr><td colspan="3">Discount conform cod reducere: xxxx-xxxx-xxxx-5089</td><td>-50,00 Lei</td></tr>
  <tr><td colspan="3">Cost livrare și procesare:</td><td>10,00 Lei</td></tr>
  <tr><td colspan="3">Total:</td><td>12,52 Lei</td></tr>
</table></td></tr>
<tr><td>Produse vândute și livrate de <a>BIZCORA</a></td></tr>
<tr><td><table>
  <tr><td>Livrare în easybox Casa Nasului</td></tr>
  <tr>
    <td><a><img src="https://s13emagst.akamaized.net/products/x/3.jpg" alt="img"></a></td>
    <td><a title="Rola pentru Scame si Par din Silicon, 17 x 10 cm Bleu">Rola...</a></td>
    <td>5&nbsp;buc</td>
    <td>111,30 LEI</td>
  </tr>
  <tr><td colspan="3">Cost livrare și procesare:</td><td>10,00 Lei</td></tr>
  <tr><td colspan="3">Total:</td><td>121,30 Lei</td></tr>
</table></td></tr>
</table>
</body></html>`

// Minimal HTML for the "Comanda ta #NNN a fost predată curierului" email.
const shippedHTML = `<html><body>
<table>
<tr><td>Produse vândute și livrate de <a>eMAG</a></td></tr>
<tr><td><table>
  <tr>
    <td><a><img src="https://s13emagst.akamaized.net/products/y/1.jpg" alt="img"></a></td>
    <td><a title="Cafea boabe, Julius Meinl Jubilaum, 500 g">Cafea...</a></td>
    <td>1&nbsp;buc</td>
    <td>63,44 Lei</td>
  </tr>
  <tr><td colspan="3">Discount conform cod reducere: xxxx-xxxx-xxxx-6635</td><td>-50,00 Lei</td></tr>
  <tr><td colspan="3">Cost livrare și procesare:</td><td>10,00 Lei</td></tr>
  <tr><td colspan="3">Servicii operationale:</td><td>1,95 Lei</td></tr>
  <tr><td colspan="3">Total:</td><td>25,39 Lei</td></tr>
</table></td></tr>
</table>
</body></html>`

// Minimal HTML for the Sameday "a ajuns în easybox" email.
const arrivedHTML = `<html><body>
<p>Hei,<br><br>Comanda ta <strong>eMAG</strong> numărul <strong>485741339</strong> a ajuns în easybox Casa Nasului.<br>
Folosește QR-ul sau PIN-ul de mai jos pentru a prelua coletul pana Vineri, 24 Apr. ora 4:30.<br><br></p>
<img src="https://sameday.ro/locker/qr-image/RP6JED9" alt="" width="200">
<p>Sau tastează pe ecranul easybox codul:</p>
<table class="easyboxCode">
<tr>
<td><span>R</span></td><td></td><td><span>P</span></td><td></td><td><span>6</span></td><td></td><td><span>J</span></td><td></td><td><span>E</span></td><td></td><td><span>D</span></td><td></td><td><span>9</span></td>
</tr>
</table>
<p>Nu ajungi la timp? Descarcă SAMEDAY App și prelungește termenul de păstrare.</p>
<table><tr>
  <td><img alt="pin_easybox" src="https://sameday.ro/newsletter/pin.png"></td>
  <td><p>easybox Casa Nasului<br>Str. Cetatuia, Nr. 12<br>Program easybox: L-D 00:00-23:59<br><br></p></td>
</tr></table>
</body></html>`

func TestClassify(t *testing.T) {
	cases := []struct {
		subject, body, want string
	}{
		{"Confirmare înregistrare comandă #485742108 ✍", "", "confirmation"},
		{"Comanda ta #485633662 a fost predată curierului", "", "shipped"},
		{"Notificare", "Hei, Comanda ta eMAG numărul 485741339 a ajuns în easybox Casa Nasului.", "arrived"},
		{"Notificare", "Hei, Comanda ta eMAG Marketplace - SOMESELLER,eMAG numărul 123 a ajuns în easybox.", ""},
		{"Random", "Random body", ""},
	}
	for _, c := range cases {
		got := ClassifyEmail(c.subject, c.body)
		if got != c.want {
			t.Errorf("classify(%q, %q) = %q, want %q", c.subject, c.body[:min(40, len(c.body))], got, c.want)
		}
	}
}

// Mirrors the real "Produse livrate de eMAG" → "Produse vândute de <Seller>"
// nesting from the 486070431 confirmation email: the outer group delivery is
// eMAG (so every product in it must be kept), while the inner seller sub-
// header names a non-eMAG seller and must NOT disqualify the products.
const confirmationNestedSellerHTML = `<html><body>
<table>
<tr><td>Produse livrate de <a>eMAG</a></td></tr>
<tr><td><table>
  <tr><td>Livrare în easybox Ariesul Mare</td></tr>
  <tr><td>Produse vândute de <a>Perfume Carnival</a></td></tr>
  <tr>
    <td><a><img src="https://s13emagst.akamaized.net/products/80621/80620880/images/x.jpg" alt="img"></a></td>
    <td><a title="Apa de Parfum Lattafa, HER CONFESSION, Dama, 100ml">Apa de Parfum Lattafa...</a></td>
    <td>1&nbsp;buc</td>
    <td>116,51 LEI</td>
  </tr>
  <tr><td colspan="3">Reducere conform voucher: xxxx-xxxx-xxxx-4326</td><td>-50,00 LEI</td></tr>
  <tr><td colspan="3">Cost livrare și procesare:</td><td>10,00 Lei</td></tr>
  <tr><td colspan="3">Servicii operationale:</td><td>1,77 Lei</td></tr>
  <tr><td colspan="3">Total:</td><td>78,28 Lei</td></tr>
</table></td></tr>
</table>
</body></html>`

func TestParseConfirmationNestedSeller(t *testing.T) {
	pe, err := ParseConfirmation("Confirmare înregistrare comandă #486070431", confirmationNestedSellerHTML)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(pe.Products) != 1 {
		t.Fatalf("expected 1 product (kept because outer group is eMAG-delivered), got %d: %+v", len(pe.Products), pe.Products)
	}
	if !strings.Contains(pe.Products[0].Name, "Lattafa") {
		t.Errorf("product name: %q", pe.Products[0].Name)
	}
	if pe.Products[0].LineTotalBani != 11651 {
		t.Errorf("product total bani: got %d, want 11651", pe.Products[0].LineTotalBani)
	}
	if pe.TotalBani != 7828 {
		t.Errorf("order total: got %d, want 7828", pe.TotalBani)
	}
}

func TestParseConfirmation(t *testing.T) {
	pe, err := ParseConfirmation("Confirmare înregistrare comandă #485742108", confirmationHTML)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if pe.OrderNumber != "485742108" {
		t.Errorf("order: got %q", pe.OrderNumber)
	}
	if len(pe.Products) != 2 {
		t.Errorf("expected 2 products, got %d: %+v", len(pe.Products), pe.Products)
	}
	for _, p := range pe.Products {
		if strings.Contains(strings.ToLower(p.Name), "rola") {
			t.Errorf("BIZCORA product leaked: %s", p.Name)
		}
	}
	if pe.TotalBani != 1252 {
		t.Errorf("total: got %d, want 1252", pe.TotalBani)
	}
}

func TestParseShipped(t *testing.T) {
	pe, err := ParseShipped("Comanda ta #485633662 a fost predată curierului", shippedHTML)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if pe.OrderNumber != "485633662" {
		t.Errorf("order: %q", pe.OrderNumber)
	}
	if len(pe.Products) != 1 {
		t.Fatalf("products: got %d", len(pe.Products))
	}
	if !strings.Contains(pe.Products[0].Name, "Cafea") {
		t.Errorf("product name: %q", pe.Products[0].Name)
	}
	if pe.Products[0].Qty != 1 || pe.Products[0].LineTotalBani != 6344 {
		t.Errorf("product qty/total: %d %d", pe.Products[0].Qty, pe.Products[0].LineTotalBani)
	}
	if pe.TotalBani != 2539 {
		t.Errorf("total: got %d, want 2539", pe.TotalBani)
	}
}

func TestParseArrived(t *testing.T) {
	text := htmlToText(arrivedHTML)
	pe, err := ParseArrived(arrivedHTML, text)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if pe.OrderNumber != "485741339" {
		t.Errorf("order: %q", pe.OrderNumber)
	}
	if pe.PinCode != "RP6JED9" {
		t.Errorf("pin: got %q", pe.PinCode)
	}
	if pe.QRURL == "" || !strings.Contains(pe.QRURL, "RP6JED9") {
		t.Errorf("qr url: %q", pe.QRURL)
	}
	if !strings.Contains(pe.EasyboxName, "Casa Nasului") {
		t.Errorf("easybox name: %q", pe.EasyboxName)
	}
	if !strings.Contains(pe.EasyboxAddress, "Cetatuia") {
		t.Errorf("easybox address: %q", pe.EasyboxAddress)
	}
	if pe.PickupDeadline == nil {
		t.Fatalf("deadline is nil")
	}
	if pe.PickupDeadline.Day() != 24 || pe.PickupDeadline.Month().String() != "April" || pe.PickupDeadline.Hour() != 4 {
		t.Errorf("deadline: %v", pe.PickupDeadline)
	}
}

func TestParseArrivedMarketplaceRejected(t *testing.T) {
	body := "Hei, Comanda ta eMAG Marketplace - SELLER XYZ,eMAG numărul 485271936 a ajuns în easybox Apusului."
	got := ClassifyEmail("whatever", body)
	if got != "" {
		t.Errorf("marketplace should be rejected, got %q", got)
	}
}

// reminderHTML mirrors the Sameday "te mai așteaptă până" reminder email
// (sent for both eMAG-direct and Marketplace orders that are still sitting in
// the easybox close to expiry).
const reminderHTML = `<html><body>
<p>Hei, coletul <strong> eMAG Marketplace - CIPRICOM SRL,eMAG numărul 485474958</strong> te mai așteaptă până Joi, 23 Apr. ora 7:30, în easybox GEMA Amada Balroom, program L-D 00:00-23:59.
Folosește QR-ul sau PIN-ul de mai jos pentru a deschide sertarul.<br><br></p>
<img src="https://sameday.ro/locker/qr-image/LUKJHZA" alt="" width="200">
<p>Sau tastează pe ecranul easybox codul:</p>
<table class="easyboxCode">
<tr>
<td><span>L</span></td><td></td><td><span>U</span></td><td></td><td><span>K</span></td><td></td><td><span>J</span></td><td></td><td><span>H</span></td><td></td><td><span>Z</span></td><td></td><td><span>A</span></td>
</tr>
</table>
</body></html>`

func TestClassifyReminder(t *testing.T) {
	body := htmlToText(reminderHTML)
	got := ClassifyEmail("Notificare 78", body)
	if got != "arrived" {
		t.Errorf("reminder should classify as arrived, got %q", got)
	}
}

func TestParseReminder(t *testing.T) {
	body := htmlToText(reminderHTML)
	pe, err := ParseArrived(reminderHTML, body)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if pe.OrderNumber != "485474958" {
		t.Errorf("order: got %q", pe.OrderNumber)
	}
	if pe.PinCode != "LUKJHZA" {
		t.Errorf("pin: got %q", pe.PinCode)
	}
	if pe.QRURL == "" || !strings.Contains(pe.QRURL, "LUKJHZA") {
		t.Errorf("qr url: %q", pe.QRURL)
	}
	if !strings.Contains(pe.EasyboxName, "GEMA Amada Balroom") {
		t.Errorf("easybox name: %q", pe.EasyboxName)
	}
	if pe.PickupDeadline == nil {
		t.Fatalf("deadline is nil")
	}
	if pe.PickupDeadline.Day() != 23 || pe.PickupDeadline.Month().String() != "April" || pe.PickupDeadline.Hour() != 7 || pe.PickupDeadline.Minute() != 30 {
		t.Errorf("deadline: %v", pe.PickupDeadline)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
