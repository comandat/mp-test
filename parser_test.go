package main

import (
	"strings"
	"testing"
)

// confirmationHTML mirrors a two-group confirmation email: the outer block
// is eMAG-delivered (sub-grouped by BIZCORA via a seller sub-header that
// must NOT start a new group), and a separate "Produse vândute și livrate
// de BIZCORA" delivery block follows — which IS a separate shipment
// delivered by BIZCORA (not eMAG).
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

// shippedHTML mirrors the simpler "Comanda ta #N a fost predată curierului"
// email: single shipment, eMAG-delivered.
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

// arrivedHTML mirrors the Sameday "a ajuns în easybox" email.
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

// reminderHTML mirrors the Sameday "te mai așteaptă până" reminder email.
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

// nestedSellerHTML mirrors the #486070431 structure: outer group delivered
// by eMAG, inner "Produse vândute de Perfume Carnival" seller sub-header
// that must NOT start a new shipment.
const nestedSellerHTML = `<html><body>
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

func TestClassify(t *testing.T) {
	cases := []struct {
		subject, body, want string
	}{
		{"Confirmare înregistrare comandă #485742108 ✍", "", "confirmation"},
		{"Comanda ta #485633662 a fost predată curierului", "", "shipped"},
		{"Notificare", "Hei, Comanda ta eMAG numărul 485741339 a ajuns în easybox Casa Nasului.", "arrived"},
		{"Notificare", "Hei, Comanda ta eMAG Marketplace - SOMESELLER,eMAG numărul 123 a ajuns în easybox Apusului.", "arrived"},
		{"Random", "Random body", ""},
	}
	for _, c := range cases {
		got := ClassifyEmail(c.subject, c.body)
		if got != c.want {
			t.Errorf("classify(%q, %q) = %q, want %q", c.subject, c.body[:min(40, len(c.body))], got, c.want)
		}
	}
}

func TestParseConfirmationTwoShipments(t *testing.T) {
	pe, err := ParseConfirmation("Confirmare înregistrare comandă #485742108", confirmationHTML)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if pe.OrderNumber != "485742108" {
		t.Errorf("order: got %q", pe.OrderNumber)
	}
	if len(pe.Shipments) != 2 {
		t.Fatalf("expected 2 shipments, got %d", len(pe.Shipments))
	}
	// shipment 0: eMAG-delivered, 2 products, total 12,52
	sh0 := pe.Shipments[0]
	if !sh0.DeliveredByEmag {
		t.Errorf("shipment 0 should be delivered by eMAG")
	}
	if sh0.EasyboxName != "Casa Nasului" {
		t.Errorf("shipment 0 easybox: %q", sh0.EasyboxName)
	}
	if len(sh0.Products) != 2 {
		t.Fatalf("shipment 0 products: %d (%+v)", len(sh0.Products), sh0.Products)
	}
	if sh0.TotalBani != 1252 {
		t.Errorf("shipment 0 total: %d", sh0.TotalBani)
	}
	// shipment 1: BIZCORA-delivered, 1 product, total 121,30
	sh1 := pe.Shipments[1]
	if sh1.DeliveredByEmag {
		t.Errorf("shipment 1 should NOT be delivered by eMAG (BIZCORA)")
	}
	if len(sh1.Products) != 1 {
		t.Fatalf("shipment 1 products: %d", len(sh1.Products))
	}
	if !strings.Contains(sh1.Products[0].Name, "Rola") {
		t.Errorf("shipment 1 product: %q", sh1.Products[0].Name)
	}
	if sh1.TotalBani != 12130 {
		t.Errorf("shipment 1 total: %d", sh1.TotalBani)
	}
	if pe.TotalBani != 1252+12130 {
		t.Errorf("order total: %d", pe.TotalBani)
	}
}

func TestParseConfirmationNestedSeller(t *testing.T) {
	pe, err := ParseConfirmation("Confirmare înregistrare comandă #486070431", nestedSellerHTML)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(pe.Shipments) != 1 {
		t.Fatalf("expected 1 shipment (seller sub-header must NOT split), got %d", len(pe.Shipments))
	}
	sh := pe.Shipments[0]
	if !sh.DeliveredByEmag {
		t.Errorf("shipment should be delivered by eMAG")
	}
	if len(sh.Products) != 1 {
		t.Fatalf("expected 1 product, got %d", len(sh.Products))
	}
	if !strings.Contains(sh.Products[0].Name, "Lattafa") {
		t.Errorf("product name: %q", sh.Products[0].Name)
	}
	if sh.Products[0].LineTotalBani != 11651 {
		t.Errorf("product line total: %d", sh.Products[0].LineTotalBani)
	}
	if sh.SellerGroup != "Perfume Carnival" {
		t.Errorf("seller: %q (want Perfume Carnival)", sh.SellerGroup)
	}
	if sh.TotalBani != 7828 {
		t.Errorf("shipment total: %d", sh.TotalBani)
	}
	if sh.EasyboxName != "Ariesul Mare" {
		t.Errorf("easybox: %q", sh.EasyboxName)
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
	if len(pe.Shipments) != 1 {
		t.Fatalf("shipments: got %d", len(pe.Shipments))
	}
	sh := pe.Shipments[0]
	if len(sh.Products) != 1 {
		t.Fatalf("products: %d", len(sh.Products))
	}
	if !strings.Contains(sh.Products[0].Name, "Cafea") {
		t.Errorf("product name: %q", sh.Products[0].Name)
	}
	if sh.Products[0].Qty != 1 || sh.Products[0].LineTotalBani != 6344 {
		t.Errorf("product qty/total: %d %d", sh.Products[0].Qty, sh.Products[0].LineTotalBani)
	}
	if sh.TotalBani != 2539 {
		t.Errorf("shipment total: %d", sh.TotalBani)
	}
	if pe.TotalBani != 2539 {
		t.Errorf("order total: %d", pe.TotalBani)
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
	if pe.ArrivalCourier != "eMAG" {
		t.Errorf("courier: got %q, want eMAG", pe.ArrivalCourier)
	}
	if !strings.Contains(pe.ArrivalEasybox, "Casa Nasului") {
		t.Errorf("easybox name: %q", pe.ArrivalEasybox)
	}
	if !strings.Contains(pe.ArrivalEasyboxAddr, "Cetatuia") {
		t.Errorf("easybox address: %q", pe.ArrivalEasyboxAddr)
	}
	if pe.PickupDeadline == nil {
		t.Fatalf("deadline is nil")
	}
	if pe.PickupDeadline.Day() != 24 || pe.PickupDeadline.Month().String() != "April" || pe.PickupDeadline.Hour() != 4 {
		t.Errorf("deadline: %v", pe.PickupDeadline)
	}
}

// marketplaceArrivedHTML mirrors a Sameday arrival email where the courier
// is a marketplace partner rather than eMAG itself. Previously these got
// rejected; now they are kept and labelled.
const marketplaceArrivedHTML = `<html><body>
<p>Hei,<br><br>
Comanda ta <strong>eMAG Marketplace - PAXYcourier s.r.o.</strong> numărul <strong>485271354</strong> a ajuns în easybox Lujerului.<br>
Folosește QR-ul sau PIN-ul de mai jos pentru a prelua coletul pana Luni, 27 Apr. ora 15:20.<br><br></p>
<img src="https://sameday.ro/locker/qr-image/L9F6Y37" alt="" width="200">
</body></html>`

func TestParseArrivedMarketplaceCourier(t *testing.T) {
	text := htmlToText(marketplaceArrivedHTML)
	if got := ClassifyEmail("Notificare", text); got != "arrived" {
		t.Fatalf("classify: got %q, want arrived", got)
	}
	pe, err := ParseArrived(marketplaceArrivedHTML, text)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if pe.OrderNumber != "485271354" {
		t.Errorf("order: %q", pe.OrderNumber)
	}
	if pe.ArrivalCourier != "eMAG Marketplace - PAXYcourier s.r.o." {
		t.Errorf("courier: got %q", pe.ArrivalCourier)
	}
	if !strings.Contains(pe.ArrivalEasybox, "Lujerului") {
		t.Errorf("easybox: %q", pe.ArrivalEasybox)
	}
	if pe.PinCode != "L9F6Y37" {
		t.Errorf("pin: %q", pe.PinCode)
	}
}

func TestClassifyReminder(t *testing.T) {
	body := htmlToText(reminderHTML)
	if got := ClassifyEmail("Notificare 78", body); got != "arrived" {
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
	if pe.ArrivalCourier != "eMAG Marketplace - CIPRICOM SRL" {
		t.Errorf("courier: got %q", pe.ArrivalCourier)
	}
	if !strings.Contains(pe.ArrivalEasybox, "GEMA Amada Balroom") {
		t.Errorf("easybox name: %q", pe.ArrivalEasybox)
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
