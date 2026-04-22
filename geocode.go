package main

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

type Geocoder struct {
	store *Store
	ua    string

	mu   sync.Mutex
	last time.Time
}

func NewGeocoder(store *Store, userAgent string) *Geocoder {
	return &Geocoder{store: store, ua: userAgent}
}

// Run periodically walks orders without coords and geocodes their easybox address.
func (g *Geocoder) Run(ctx context.Context) {
	// First pass shortly after startup, then every 2 minutes.
	t := time.NewTimer(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		addresses, err := g.store.AddressesWithoutCoords(ctx)
		if err != nil {
			log.Printf("geocode: list: %v", err)
		} else {
			for _, addr := range addresses {
				if err := ctx.Err(); err != nil {
					return
				}
				lat, lon, ok := g.lookup(ctx, addr)
				if !ok {
					continue
				}
				if err := g.store.GeocodeSave(ctx, addr, lat, lon); err != nil {
					log.Printf("geocode: save: %v", err)
				}
				if err := g.store.ApplyCoordsToAddress(ctx, addr, lat, lon); err != nil {
					log.Printf("geocode: apply: %v", err)
				}
				log.Printf("geocode: %s -> %.5f,%.5f", addr, lat, lon)
			}
		}
		t.Reset(2 * time.Minute)
	}
}

func (g *Geocoder) lookup(ctx context.Context, address string) (float64, float64, bool) {
	// Cache hit?
	if lat, lon, ok, err := g.store.GeocodeLookup(ctx, address); err == nil && ok {
		return lat, lon, true
	}
	// Rate limit: at most 1 req / 1.1s.
	g.mu.Lock()
	if elapsed := time.Since(g.last); elapsed < 1100*time.Millisecond {
		time.Sleep(1100*time.Millisecond - elapsed)
	}
	g.last = time.Now()
	g.mu.Unlock()

	u := "https://nominatim.openstreetmap.org/search?" + url.Values{
		"q":            {address + ", Romania"},
		"format":       {"json"},
		"limit":        {"1"},
		"countrycodes": {"ro"},
	}.Encode()
	req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
	req.Header.Set("User-Agent", g.ua)
	req.Header.Set("Accept-Language", "ro,en;q=0.8")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("geocode: http: %v", err)
		return 0, 0, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		log.Printf("geocode: status %d for %s", resp.StatusCode, address)
		return 0, 0, false
	}
	body, _ := io.ReadAll(resp.Body)
	var arr []struct {
		Lat string `json:"lat"`
		Lon string `json:"lon"`
	}
	if err := json.Unmarshal(body, &arr); err != nil {
		log.Printf("geocode: parse: %v", err)
		return 0, 0, false
	}
	if len(arr) == 0 {
		log.Printf("geocode: no results for %s", address)
		return 0, 0, false
	}
	lat, err1 := strconv.ParseFloat(arr[0].Lat, 64)
	lon, err2 := strconv.ParseFloat(arr[0].Lon, 64)
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return lat, lon, true
}
