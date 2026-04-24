package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

//go:embed templates/*.html static/*
var assetsFS embed.FS

type Web struct {
	store *Store
	sync  *AgentMailSync
	tpl   *template.Template
}

func NewWeb(store *Store, sync *AgentMailSync) (*Web, error) {
	funcs := template.FuncMap{
		"lei": func(bani int64) string {
			return formatLei(bani)
		},
		"statusClass": func(s OrderStatus) string {
			switch s {
			case StatusArrived:
				return "status-arrived"
			case StatusShipped:
				return "status-shipped"
			case StatusRegistered:
				return "status-registered"
			}
			return ""
		},
		"statusLabel": func(s OrderStatus) string {
			switch s {
			case StatusArrived:
				return "Gata de ridicare"
			case StatusShipped:
				return "În livrare"
			case StatusRegistered:
				return "Înregistrată"
			}
			return string(s)
		},
		"relativeDeadline": func(t *time.Time) string {
			if t == nil {
				return ""
			}
			loc, _ := time.LoadLocation("Europe/Bucharest")
			if loc == nil {
				loc = time.UTC
			}
			local := t.In(loc)
			d := time.Until(local)
			if d < 0 {
				return "EXPIRAT " + local.Format("2 Jan 15:04")
			}
			if d < time.Hour {
				return fmt.Sprintf("în %d min", int(d.Minutes()))
			}
			if d < 24*time.Hour {
				h := int(d.Hours())
				m := int(d.Minutes()) - h*60
				return fmt.Sprintf("în %dh %02dm", h, m)
			}
			days := int(d.Hours() / 24)
			return fmt.Sprintf("în %d zile (%s)", days, local.Format("2 Jan 15:04"))
		},
		"formatDeadline": func(t *time.Time) string {
			if t == nil {
				return ""
			}
			loc, _ := time.LoadLocation("Europe/Bucharest")
			if loc == nil {
				loc = time.UTC
			}
			return t.In(loc).Format("Mon 2 Jan, 15:04")
		},
		"activeShipments": func(shipments []Shipment) []Shipment {
			out := make([]Shipment, 0, len(shipments))
			for _, sh := range shipments {
				if sh.Active() {
					out = append(out, sh)
				}
			}
			return out
		},
		"dismissedShipments": func(shipments []Shipment) []Shipment {
			out := make([]Shipment, 0, len(shipments))
			for _, sh := range shipments {
				if sh.DismissedAt != nil {
					out = append(out, sh)
				}
			}
			return out
		},
		"pickedUpShipments": func(shipments []Shipment) []Shipment {
			out := make([]Shipment, 0, len(shipments))
			for _, sh := range shipments {
				if sh.PickedUpAt != nil {
					out = append(out, sh)
				}
			}
			return out
		},
		"shipmentLabel": func(sh Shipment) string {
			seller := strings.TrimSpace(sh.SellerGroup)
			delivery := strings.TrimSpace(sh.DeliveryBy)
			if seller == "" && delivery == "" {
				return "Livrare"
			}
			if seller == "" || strings.EqualFold(seller, delivery) {
				return "Livrat de " + delivery
			}
			if delivery == "" {
				return "Vândut de " + seller
			}
			return seller + " · livrat de " + delivery
		},
	}

	tpl, err := template.New("").Funcs(funcs).ParseFS(assetsFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	return &Web{store: store, sync: sync, tpl: tpl}, nil
}

func (w *Web) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", w.handleHealth)
	mux.HandleFunc("/map", w.handleMap)
	mux.HandleFunc("/api/pins", w.handlePins)
	mux.HandleFunc("/order/", w.handleOrder)
	mux.HandleFunc("/debug/emails", w.handleDebugList)
	mux.HandleFunc("/debug/email", w.handleDebugFetch)
	mux.Handle("/static/", http.FileServer(http.FS(assetsFS)))
	mux.HandleFunc("/", w.handleList)
	return logMiddleware(securityHeaders(mux))
}

// /debug/emails?kind=confirmation&order=NNN
//
// Lists every message we've stored in emails_processed (most recent first).
// Each row links to /debug/email?id=... which streams the raw HTML straight
// from AgentMail so we can compare it against the parser's expectations.
func (w *Web) handleDebugList(rw http.ResponseWriter, r *http.Request) {
	kind := r.URL.Query().Get("kind")
	order := r.URL.Query().Get("order")
	rows, err := w.store.ListProcessedEmails(r.Context(), kind, order, 500)
	if err != nil {
		http.Error(rw, err.Error(), 500)
		return
	}
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(rw, `<!DOCTYPE html><html><body style="font-family:monospace;padding:16px;">
<h2>emails_processed (%d rows)</h2>
<form method="GET" style="margin:8px 0;">
  <label>kind: <input name="kind" value="%s" placeholder="confirmation|shipped|arrived"></label>
  <label>order: <input name="order" value="%s"></label>
  <button>filter</button>
</form>
<table border="1" cellpadding="4" cellspacing="0">
<tr><th>processed_at</th><th>kind</th><th>order</th><th>message_id</th><th>html</th><th>text</th></tr>`,
		len(rows), template.HTMLEscapeString(kind), template.HTMLEscapeString(order))
	for _, pe := range rows {
		idEsc := template.HTMLEscapeString(pe.MessageID)
		fmt.Fprintf(rw, `<tr>
<td>%s</td><td>%s</td><td>%s</td><td>%s</td>
<td><a href="/debug/email?id=%s&format=html">html</a></td>
<td><a href="/debug/email?id=%s&format=text">text</a></td>
</tr>`,
			pe.ProcessedAt.Format("2006-01-02 15:04:05"),
			template.HTMLEscapeString(pe.Kind),
			template.HTMLEscapeString(pe.OrderNumber),
			idEsc, idEsc, idEsc)
	}
	fmt.Fprintf(rw, `</table></body></html>`)
}

// /debug/email?id=<message_id>&format=html|text|raw
//
// Streams the AgentMail message body so we can see exactly what the parser
// is fed. format=html returns the raw HTML as text/plain (so the browser
// shows source instead of rendering it); format=text returns text/plain
// of the message text body.
func (w *Web) handleDebugFetch(rw http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(rw, "missing id", 400)
		return
	}
	if w.sync == nil {
		http.Error(rw, "agentmail sync not configured", 500)
		return
	}
	subject, htmlBody, textBody, err := w.sync.FetchRaw(r.Context(), id)
	if err != nil {
		http.Error(rw, "fetch: "+err.Error(), 502)
		return
	}
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "html"
	}
	rw.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(rw, "subject: %s\nmessage_id: %s\nformat: %s\n\n----- BODY -----\n", subject, id, format)
	if format == "text" {
		rw.Write([]byte(textBody))
		return
	}
	rw.Write([]byte(htmlBody))
}

func (w *Web) handleHealth(rw http.ResponseWriter, r *http.Request) {
	rw.Write([]byte("ok"))
}

func (w *Web) handleList(rw http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(rw, r)
		return
	}
	ctx := r.Context()
	orders, err := w.store.ListActiveOrders(ctx)
	if err != nil {
		http.Error(rw, err.Error(), 500)
		return
	}
	if err := w.tpl.ExecuteTemplate(rw, "list.html", map[string]interface{}{"Orders": orders}); err != nil {
		log.Printf("web: render list: %v", err)
	}
}

// /order/{num}
// /order/{num}/shipment/{id}/dismiss
// /order/{num}/shipment/{id}/restore
// /order/{num}/shipment/{id}/confirm
func (w *Web) handleOrder(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	path := strings.TrimPrefix(r.URL.Path, "/order/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(rw, r)
		return
	}
	orderNum := parts[0]

	if len(parts) >= 4 && parts[1] == "shipment" {
		if r.Method != http.MethodPost {
			http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		shipmentID, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil {
			http.NotFound(rw, r)
			return
		}
		switch parts[3] {
		case "dismiss":
			if err := w.store.DismissShipment(ctx, orderNum, shipmentID); err != nil {
				http.Error(rw, err.Error(), 500)
				return
			}
		case "restore":
			if err := w.store.RestoreShipment(ctx, orderNum, shipmentID); err != nil {
				http.Error(rw, err.Error(), 500)
				return
			}
		case "confirm":
			if err := w.store.MarkShipmentPickedUp(ctx, orderNum, shipmentID); err != nil {
				http.Error(rw, err.Error(), 500)
				return
			}
		default:
			http.NotFound(rw, r)
			return
		}
		http.Redirect(rw, r, "/order/"+orderNum, http.StatusSeeOther)
		return
	}

	o, err := w.store.GetOrder(ctx, orderNum)
	if err != nil {
		http.Error(rw, err.Error(), 500)
		return
	}
	if o == nil {
		http.NotFound(rw, r)
		return
	}
	if err := w.tpl.ExecuteTemplate(rw, "detail.html", map[string]interface{}{"Order": o}); err != nil {
		log.Printf("web: render detail: %v", err)
	}
}

func (w *Web) handleMap(rw http.ResponseWriter, r *http.Request) {
	if err := w.tpl.ExecuteTemplate(rw, "map.html", nil); err != nil {
		log.Printf("web: render map: %v", err)
	}
}

type pinOut struct {
	OrderNumber    string  `json:"order_number"`
	Lat            float64 `json:"lat"`
	Lon            float64 `json:"lon"`
	EasyboxName    string  `json:"easybox_name"`
	EasyboxAddress string  `json:"easybox_address"`
	Status         string  `json:"status"`
	Deadline       string  `json:"deadline"`
}

func (w *Web) handlePins(rw http.ResponseWriter, r *http.Request) {
	orders, err := w.store.ListActiveOrders(r.Context())
	if err != nil {
		http.Error(rw, err.Error(), 500)
		return
	}
	var pins []pinOut
	for _, o := range orders {
		for _, sh := range o.Shipments {
			if !sh.Active() || sh.Lat == nil || sh.Lon == nil {
				continue
			}
			status := string(o.Status)
			if sh.HasPickup() {
				status = string(StatusArrived)
			}
			p := pinOut{
				OrderNumber:    o.OrderNumber,
				Lat:            *sh.Lat,
				Lon:            *sh.Lon,
				EasyboxName:    sh.EasyboxName,
				EasyboxAddress: sh.EasyboxAddress,
				Status:         status,
			}
			if sh.PickupDeadline != nil {
				p.Deadline = sh.PickupDeadline.Format(time.RFC3339)
			}
			pins = append(pins, p)
		}
	}
	rw.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(rw).Encode(pins)
}

func securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("X-Robots-Tag", "noindex, nofollow")
		rw.Header().Set("Referrer-Policy", "no-referrer")
		h.ServeHTTP(rw, r)
	})
}

func logMiddleware(h http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		start := time.Now()
		h.ServeHTTP(rw, r)
		if r.URL.Path != "/healthz" {
			log.Printf("http %s %s (%s)", r.Method, r.URL.Path, time.Since(start))
		}
	})
}

func formatLei(bani int64) string {
	sign := ""
	if bani < 0 {
		sign = "-"
		bani = -bani
	}
	major := bani / 100
	minor := bani % 100
	return fmt.Sprintf("%s%d,%02d Lei", sign, major, minor)
}
