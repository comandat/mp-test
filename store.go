package main

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

type OrderStatus string

const (
	StatusRegistered OrderStatus = "Inregistrata"
	StatusShipped    OrderStatus = "InLivrare"
	StatusArrived    OrderStatus = "GataDeRidicare"
)

func statusRank(s OrderStatus) int {
	switch s {
	case StatusRegistered:
		return 1
	case StatusShipped:
		return 2
	case StatusArrived:
		return 3
	}
	return 0
}

type Order struct {
	OrderNumber     string
	Status          OrderStatus
	EasyboxName     string
	EasyboxAddress  string
	PickupDeadline  *time.Time
	PinCode         string
	QRURL           string
	TotalBani       int64
	Lat             *float64
	Lon             *float64
	PickedUpAt      *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
	Products        []Product
}

type Product struct {
	Name         string
	ImageURL     string
	Qty          int
	LineTotalBani int64
	SellerGroup  string
}

type Store struct {
	db *sql.DB
}

func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS orders(
			order_number TEXT PRIMARY KEY,
			status TEXT NOT NULL,
			easybox_name TEXT,
			easybox_address TEXT,
			pickup_deadline TEXT,
			pin_code TEXT,
			qr_url TEXT,
			total_bani INTEGER NOT NULL DEFAULT 0,
			lat REAL,
			lon REAL,
			picked_up_at TEXT,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS products(
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			order_number TEXT NOT NULL,
			name TEXT NOT NULL,
			image_url TEXT,
			qty INTEGER NOT NULL,
			line_total_bani INTEGER NOT NULL,
			seller_group TEXT,
			FOREIGN KEY(order_number) REFERENCES orders(order_number) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS products_order_idx ON products(order_number)`,
		`CREATE TABLE IF NOT EXISTS emails_processed(
			message_id TEXT PRIMARY KEY,
			kind TEXT,
			order_number TEXT,
			processed_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS geocode_cache(
			address TEXT PRIMARY KEY,
			lat REAL,
			lon REAL,
			fetched_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS proton_session(
			id INTEGER PRIMARY KEY CHECK(id=1),
			uid TEXT,
			access_token TEXT,
			refresh_token TEXT,
			last_event_id TEXT,
			updated_at TEXT NOT NULL
		)`,
	}
	for _, q := range stmts {
		if _, err := s.db.Exec(q); err != nil {
			return fmt.Errorf("migrate: %w: %s", err, q)
		}
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

// ---------- Proton session ----------

type Session struct {
	UID          string
	AccessToken  string
	RefreshToken string
	LastEventID  string
}

func (s *Store) LoadSession(ctx context.Context) (*Session, error) {
	row := s.db.QueryRowContext(ctx, `SELECT uid, access_token, refresh_token, COALESCE(last_event_id,'') FROM proton_session WHERE id=1`)
	var sess Session
	if err := row.Scan(&sess.UID, &sess.AccessToken, &sess.RefreshToken, &sess.LastEventID); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &sess, nil
}

func (s *Store) SaveSessionAuth(ctx context.Context, uid, accessToken, refreshToken string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO proton_session(id, uid, access_token, refresh_token, updated_at)
		VALUES(1, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			uid=excluded.uid, access_token=excluded.access_token,
			refresh_token=excluded.refresh_token, updated_at=excluded.updated_at`,
		uid, accessToken, refreshToken, time.Now().UTC().Format(time.RFC3339))
	return err
}

func (s *Store) SaveLastEventID(ctx context.Context, eventID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE proton_session SET last_event_id=?, updated_at=? WHERE id=1`,
		eventID, time.Now().UTC().Format(time.RFC3339))
	return err
}

// ---------- Emails processed ----------

func (s *Store) IsProcessed(ctx context.Context, messageID string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM emails_processed WHERE message_id=?`, messageID).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) MarkProcessed(ctx context.Context, messageID, kind, orderNum string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO emails_processed(message_id, kind, order_number, processed_at)
		VALUES(?, ?, ?, ?)
		ON CONFLICT(message_id) DO NOTHING`,
		messageID, kind, orderNum, time.Now().UTC().Format(time.RFC3339))
	return err
}

// ---------- Orders ----------

func (s *Store) GetOrder(ctx context.Context, orderNum string) (*Order, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT order_number, status, COALESCE(easybox_name,''), COALESCE(easybox_address,''),
		       pickup_deadline, COALESCE(pin_code,''), COALESCE(qr_url,''),
		       total_bani, lat, lon, picked_up_at, created_at, updated_at
		FROM orders WHERE order_number=?`, orderNum)
	var o Order
	var deadlineStr, pickedStr sql.NullString
	var lat, lon sql.NullFloat64
	var createdStr, updatedStr string
	var status string
	if err := row.Scan(&o.OrderNumber, &status, &o.EasyboxName, &o.EasyboxAddress,
		&deadlineStr, &o.PinCode, &o.QRURL,
		&o.TotalBani, &lat, &lon, &pickedStr, &createdStr, &updatedStr); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	o.Status = OrderStatus(status)
	if deadlineStr.Valid && deadlineStr.String != "" {
		t, _ := time.Parse(time.RFC3339, deadlineStr.String)
		o.PickupDeadline = &t
	}
	if pickedStr.Valid && pickedStr.String != "" {
		t, _ := time.Parse(time.RFC3339, pickedStr.String)
		o.PickedUpAt = &t
	}
	if lat.Valid {
		v := lat.Float64
		o.Lat = &v
	}
	if lon.Valid {
		v := lon.Float64
		o.Lon = &v
	}
	o.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
	o.UpdatedAt, _ = time.Parse(time.RFC3339, updatedStr)

	prods, err := s.getProducts(ctx, orderNum)
	if err != nil {
		return nil, err
	}
	o.Products = prods
	return &o, nil
}

func (s *Store) getProducts(ctx context.Context, orderNum string) ([]Product, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT name, COALESCE(image_url,''), qty, line_total_bani, COALESCE(seller_group,'')
		FROM products WHERE order_number=? ORDER BY id`, orderNum)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Product
	for rows.Next() {
		var p Product
		if err := rows.Scan(&p.Name, &p.ImageURL, &p.Qty, &p.LineTotalBani, &p.SellerGroup); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) ListActiveOrders(ctx context.Context) ([]*Order, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT order_number FROM orders
		WHERE picked_up_at IS NULL
		ORDER BY
			CASE status WHEN 'GataDeRidicare' THEN 1 WHEN 'InLivrare' THEN 2 WHEN 'Inregistrata' THEN 3 ELSE 4 END,
			COALESCE(pickup_deadline, '9999') ASC,
			created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var nums []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		nums = append(nums, n)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []*Order
	for _, n := range nums {
		o, err := s.GetOrder(ctx, n)
		if err != nil {
			return nil, err
		}
		if o != nil {
			out = append(out, o)
		}
	}
	return out, nil
}

// UpsertFromEmail applies an email-derived update to an order, respecting state ordering.
// kind: "confirmation" | "shipped" | "arrived"
func (s *Store) UpsertFromEmail(ctx context.Context, kind string, p *ParsedEmail) error {
	now := time.Now().UTC().Format(time.RFC3339)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var existingStatus string
	var pickedUp sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT status, picked_up_at FROM orders WHERE order_number=?`, p.OrderNumber).
		Scan(&existingStatus, &pickedUp)
	isNew := err == sql.ErrNoRows
	if err != nil && err != sql.ErrNoRows {
		return err
	}

	// Do not resurrect picked-up orders.
	if pickedUp.Valid && pickedUp.String != "" {
		return tx.Commit()
	}

	var newStatus OrderStatus
	switch kind {
	case "confirmation":
		newStatus = StatusRegistered
	case "shipped":
		newStatus = StatusShipped
	case "arrived":
		newStatus = StatusArrived
	default:
		return fmt.Errorf("unknown email kind: %s", kind)
	}

	// State never moves backward.
	finalStatus := newStatus
	if !isNew {
		if statusRank(OrderStatus(existingStatus)) > statusRank(newStatus) {
			finalStatus = OrderStatus(existingStatus)
		}
	}

	if isNew {
		_, err = tx.ExecContext(ctx, `
			INSERT INTO orders(order_number, status, total_bani, created_at, updated_at)
			VALUES(?, ?, ?, ?, ?)`,
			p.OrderNumber, string(finalStatus), p.TotalBani, now, now)
		if err != nil {
			return err
		}
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE orders SET status=?, updated_at=? WHERE order_number=?`,
			string(finalStatus), now, p.OrderNumber)
		if err != nil {
			return err
		}
	}

	// Update total from any email that has it.
	if p.TotalBani > 0 {
		_, err = tx.ExecContext(ctx, `UPDATE orders SET total_bani=?, updated_at=? WHERE order_number=?`,
			p.TotalBani, now, p.OrderNumber)
		if err != nil {
			return err
		}
	}

	// Arrived email carries PIN, QR, deadline, easybox info — overwrite with latest.
	if kind == "arrived" {
		var deadline sql.NullString
		if p.PickupDeadline != nil {
			deadline = sql.NullString{String: p.PickupDeadline.UTC().Format(time.RFC3339), Valid: true}
		}
		_, err = tx.ExecContext(ctx, `
			UPDATE orders
			SET easybox_name=?, easybox_address=?, pickup_deadline=?, pin_code=?, qr_url=?, updated_at=?
			WHERE order_number=?`,
			p.EasyboxName, p.EasyboxAddress, deadline, p.PinCode, p.QRURL, now, p.OrderNumber)
		if err != nil {
			return err
		}
	}

	// Replace product list from the latest confirmation/shipped email that has products.
	if len(p.Products) > 0 && (kind == "confirmation" || kind == "shipped") {
		_, err = tx.ExecContext(ctx, `DELETE FROM products WHERE order_number=?`, p.OrderNumber)
		if err != nil {
			return err
		}
		for _, pr := range p.Products {
			_, err = tx.ExecContext(ctx, `
				INSERT INTO products(order_number, name, image_url, qty, line_total_bani, seller_group)
				VALUES(?,?,?,?,?,?)`,
				p.OrderNumber, pr.Name, pr.ImageURL, pr.Qty, pr.LineTotalBani, pr.SellerGroup)
			if err != nil {
				return err
			}
		}
	}

	return tx.Commit()
}

func (s *Store) MarkPickedUp(ctx context.Context, orderNum string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE orders SET picked_up_at=?, updated_at=? WHERE order_number=?`,
		time.Now().UTC().Format(time.RFC3339), time.Now().UTC().Format(time.RFC3339), orderNum)
	return err
}

// ---------- Geocode cache ----------

func (s *Store) GeocodeLookup(ctx context.Context, address string) (lat, lon float64, ok bool, err error) {
	row := s.db.QueryRowContext(ctx, `SELECT lat, lon FROM geocode_cache WHERE address=?`, address)
	var la, lo sql.NullFloat64
	if err := row.Scan(&la, &lo); err != nil {
		if err == sql.ErrNoRows {
			return 0, 0, false, nil
		}
		return 0, 0, false, err
	}
	if !la.Valid || !lo.Valid {
		return 0, 0, false, nil
	}
	return la.Float64, lo.Float64, true, nil
}

func (s *Store) GeocodeSave(ctx context.Context, address string, lat, lon float64) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO geocode_cache(address, lat, lon, fetched_at) VALUES(?,?,?,?)
		ON CONFLICT(address) DO UPDATE SET lat=excluded.lat, lon=excluded.lon, fetched_at=excluded.fetched_at`,
		address, lat, lon, time.Now().UTC().Format(time.RFC3339))
	return err
}

// AddressesWithoutCoords returns distinct easybox addresses of active orders lacking lat/lon.
func (s *Store) AddressesWithoutCoords(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT easybox_address FROM orders
		WHERE picked_up_at IS NULL AND easybox_address IS NOT NULL AND easybox_address <> ''
		  AND (lat IS NULL OR lon IS NULL)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ApplyCoordsToAddress sets lat/lon on every active order matching the given address.
func (s *Store) ApplyCoordsToAddress(ctx context.Context, address string, lat, lon float64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE orders SET lat=?, lon=?, updated_at=?
		WHERE easybox_address=? AND picked_up_at IS NULL`,
		lat, lon, time.Now().UTC().Format(time.RFC3339), address)
	return err
}
