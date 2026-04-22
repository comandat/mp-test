package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strings"
	"time"
)

//go:embed templates/*.html static/*
var assetsFS embed.FS

type Web struct {
	store *Store
	tpl   *template.Template
}

func NewWeb(store *Store) (*Web, error) {
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
	}

	tpl, err := template.New("").Funcs(funcs).ParseFS(assetsFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	return &Web{store: store, tpl: tpl}, nil
}

func (w *Web) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", w.handleHealth)
	mux.HandleFunc("/map", w.handleMap)
	mux.HandleFunc("/api/pins", w.handlePins)
	mux.HandleFunc("/order/", w.handleOrder) // /order/{num} or /order/{num}/confirm
	mux.Handle("/static/", http.FileServer(http.FS(assetsFS)))
	mux.HandleFunc("/", w.handleList)
	return logMiddleware(securityHeaders(mux))
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
	data := map[string]interface{}{
		"Orders": orders,
	}
	if err := w.tpl.ExecuteTemplate(rw, "list.html", data); err != nil {
		log.Printf("web: render list: %v", err)
	}
}

func (w *Web) handleOrder(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	path := strings.TrimPrefix(r.URL.Path, "/order/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(rw, r)
		return
	}
	orderNum := parts[0]

	if len(parts) == 2 && parts[1] == "confirm" {
		if r.Method != http.MethodPost {
			http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := w.store.MarkPickedUp(ctx, orderNum); err != nil {
			http.Error(rw, err.Error(), 500)
			return
		}
		http.Redirect(rw, r, "/", http.StatusSeeOther)
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
		if o.Lat == nil || o.Lon == nil {
			continue
		}
		p := pinOut{
			OrderNumber:    o.OrderNumber,
			Lat:            *o.Lat,
			Lon:            *o.Lon,
			EasyboxName:    o.EasyboxName,
			EasyboxAddress: o.EasyboxAddress,
			Status:         string(o.Status),
		}
		if o.PickupDeadline != nil {
			p.Deadline = o.PickupDeadline.Format(time.RFC3339)
		}
		pins = append(pins, p)
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
