package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"
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

// Order is an eMAG order with one or more shipments. Aggregate fields
// (TotalBani, PickupDeadline, EasyboxName…) are derived from active
// shipments to keep list/map rendering simple.
type Order struct {
	OrderNumber string
	Status      OrderStatus
	TotalBani   int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
	Shipments   []Shipment

	// Aggregates for UI shortcuts.
	EasyboxName    string
	EasyboxAddress string
	PickupDeadline *time.Time
	Lat            *float64
	Lon            *float64
}

// Shipment is one delivery group inside an order — products, total, easybox,
// pickup PIN/QR, and user dismissal state. Arrival emails attach per-shipment
// PIN/QR/deadline by matching easybox name.
type Shipment struct {
	ID              int64
	OrderNumber     string
	GroupIndex      int
	SellerGroup     string
	DeliveryBy      string
	DeliveredByEmag bool
	EasyboxName     string
	EasyboxAddress  string
	Lat             *float64
	Lon             *float64
	PickupDeadline  *time.Time
	PinCode         string
	QRURL           string
	CourierLabel    string // who actually shipped it, from the arrival email
	TotalBani       int64
	PickedUpAt      *time.Time
	DismissedAt     *time.Time
	Products        []Product
}

// HasPickup is true once Sameday has delivered the shipment to the easybox.
func (s Shipment) HasPickup() bool { return s.PinCode != "" || s.QRURL != "" }

// Active shipments are those that still need pickup.
func (s Shipment) Active() bool { return s.DismissedAt == nil && s.PickedUpAt == nil }

type Product struct {
	Name          string
	ImageURL      string
	Qty           int
	LineTotalBani int64
	SellerGroup   string
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

func (s *Store) Close() error { return s.db.Close() }

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
		`CREATE TABLE IF NOT EXISTS shipments(
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			order_number TEXT NOT NULL,
			group_index INTEGER NOT NULL,
			seller_group TEXT,
			delivery_by TEXT,
			delivered_by_emag INTEGER NOT NULL DEFAULT 0,
			easybox_name TEXT,
			easybox_address TEXT,
			lat REAL,
			lon REAL,
			pickup_deadline TEXT,
			pin_code TEXT,
			qr_url TEXT,
			courier_label TEXT,
			total_bani INTEGER NOT NULL DEFAULT 0,
			picked_up_at TEXT,
			dismissed_at TEXT,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			UNIQUE(order_number, group_index),
			FOREIGN KEY(order_number) REFERENCES orders(order_number) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS shipments_order_idx ON shipments(order_number)`,
		`CREATE TABLE IF NOT EXISTS products(
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			order_number TEXT,
			shipment_id INTEGER,
			name TEXT NOT NULL,
			image_url TEXT,
			qty INTEGER NOT NULL,
			line_total_bani INTEGER NOT NULL,
			seller_group TEXT,
			FOREIGN KEY(shipment_id) REFERENCES shipments(id) ON DELETE CASCADE
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
	}
	for _, q := range stmts {
		if _, err := s.db.Exec(q); err != nil {
			return fmt.Errorf("migrate: %w: %s", err, q)
		}
	}

	// Older databases had products.order_number but no shipment_id column,
	// because shipments didn't exist yet. Add it if missing.
	if _, err := s.db.Exec(`ALTER TABLE products ADD COLUMN shipment_id INTEGER`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column name") {
			return fmt.Errorf("migrate: add shipment_id: %w", err)
		}
	}
	// Index must be created after shipment_id exists (ALTER TABLE above).
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS products_shipment_idx ON products(shipment_id)`); err != nil {
		return fmt.Errorf("migrate: products_shipment_idx: %w", err)
	}
	// courier_label was added after the initial shipments schema shipped.
	if _, err := s.db.Exec(`ALTER TABLE shipments ADD COLUMN courier_label TEXT`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column name") {
			return fmt.Errorf("migrate: add courier_label: %w", err)
		}
	}

	if err := s.backfillShipments(context.Background()); err != nil {
		return err
	}

	// Self-heal: only the (a) case — emails were marked processed before
	// the shipments table existed. Wipe emails_processed so the next scan
	// replays everything. Guarded by a one-shot flag in _meta so we can't
	// loop on it. The (b) "shipments without products" case is handled
	// instead by inspecting raw emails via /debug/emails.
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS _meta(key TEXT PRIMARY KEY, value TEXT)`); err != nil {
		return fmt.Errorf("migrate: _meta: %w", err)
	}
	var done string
	_ = s.db.QueryRow(`SELECT value FROM _meta WHERE key='self_heal_v1'`).Scan(&done)
	if done == "" {
		var shipCount, procCount int
		_ = s.db.QueryRow(`SELECT COUNT(*) FROM shipments`).Scan(&shipCount)
		_ = s.db.QueryRow(`SELECT COUNT(*) FROM emails_processed`).Scan(&procCount)
		if shipCount == 0 && procCount > 0 {
			log.Printf("migrate: self-heal — shipments empty, clearing %d emails_processed entries", procCount)
			if _, err := s.db.Exec(`DELETE FROM emails_processed`); err != nil {
				return fmt.Errorf("migrate: reset emails_processed: %w", err)
			}
		}
		if _, err := s.db.Exec(`INSERT INTO _meta(key, value) VALUES('self_heal_v1', ?)`,
			time.Now().UTC().Format(time.RFC3339)); err != nil {
			return fmt.Errorf("migrate: mark self_heal_v1: %w", err)
		}
	}
	return nil
}

// backfillShipments converts legacy single-delivery orders (from before the
// shipments table existed) into one synthetic shipment each, carrying over
// their easybox / PIN / QR / total fields so the UI keeps working.
func (s *Store) backfillShipments(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT o.order_number,
		       COALESCE(o.easybox_name,''), COALESCE(o.easybox_address,''),
		       o.pickup_deadline, COALESCE(o.pin_code,''), COALESCE(o.qr_url,''),
		       o.lat, o.lon, o.picked_up_at,
		       o.total_bani, o.created_at, o.updated_at
		FROM orders o
		WHERE NOT EXISTS (SELECT 1 FROM shipments WHERE order_number = o.order_number)
		  AND EXISTS (SELECT 1 FROM products WHERE order_number = o.order_number)`)
	if err != nil {
		return fmt.Errorf("backfill list: %w", err)
	}
	defer rows.Close()

	type legacy struct {
		orderNum, easyName, easyAddr, pinCode, qrURL, createdAt, updatedAt string
		deadline, pickedUp                                                 sql.NullString
		lat, lon                                                           sql.NullFloat64
		totalBani                                                          int64
	}
	var legacies []legacy
	for rows.Next() {
		var r legacy
		if err := rows.Scan(&r.orderNum, &r.easyName, &r.easyAddr,
			&r.deadline, &r.pinCode, &r.qrURL,
			&r.lat, &r.lon, &r.pickedUp,
			&r.totalBani, &r.createdAt, &r.updatedAt); err != nil {
			return err
		}
		legacies = append(legacies, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, r := range legacies {
		res, err := s.db.ExecContext(ctx, `
			INSERT INTO shipments(
				order_number, group_index,
				seller_group, delivery_by, delivered_by_emag,
				easybox_name, easybox_address, lat, lon,
				pickup_deadline, pin_code, qr_url,
				total_bani, picked_up_at,
				created_at, updated_at)
			VALUES(?, 0, 'eMAG', 'eMAG', 1,
			       NULLIF(?, ''), NULLIF(?, ''), ?, ?,
			       ?, NULLIF(?, ''), NULLIF(?, ''),
			       ?, ?,
			       ?, ?)`,
			r.orderNum,
			r.easyName, r.easyAddr, r.lat, r.lon,
			r.deadline, r.pinCode, r.qrURL,
			r.totalBani, r.pickedUp,
			r.createdAt, r.updatedAt)
		if err != nil {
			return fmt.Errorf("backfill insert shipment: %w", err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `
			UPDATE products SET shipment_id = ? WHERE order_number = ? AND shipment_id IS NULL`,
			id, r.orderNum); err != nil {
			return fmt.Errorf("backfill update products: %w", err)
		}
	}
	return nil
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

// ProcessedEmail is a row from emails_processed, used by debug endpoints to
// list what's already been consumed and look up message_ids.
type ProcessedEmail struct {
	MessageID   string
	Kind        string
	OrderNumber string
	ProcessedAt time.Time
}

// ListProcessedEmails returns the most-recent N rows from emails_processed,
// filtered by kind and/or order_number when provided.
func (s *Store) ListProcessedEmails(ctx context.Context, kind, orderNum string, limit int) ([]ProcessedEmail, error) {
	if limit <= 0 {
		limit = 200
	}
	q := `SELECT message_id, COALESCE(kind,''), COALESCE(order_number,''), processed_at
	      FROM emails_processed
	      WHERE 1=1`
	args := []any{}
	if kind != "" {
		q += ` AND kind = ?`
		args = append(args, kind)
	}
	if orderNum != "" {
		q += ` AND order_number = ?`
		args = append(args, orderNum)
	}
	q += ` ORDER BY processed_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProcessedEmail
	for rows.Next() {
		var pe ProcessedEmail
		var ts string
		if err := rows.Scan(&pe.MessageID, &pe.Kind, &pe.OrderNumber, &ts); err != nil {
			return nil, err
		}
		pe.ProcessedAt, _ = time.Parse(time.RFC3339, ts)
		out = append(out, pe)
	}
	return out, rows.Err()
}

func (s *Store) MarkProcessed(ctx context.Context, messageID, kind, orderNum string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO emails_processed(message_id, kind, order_number, processed_at)
		VALUES(?, ?, ?, ?)
		ON CONFLICT(message_id) DO NOTHING`,
		messageID, kind, orderNum, time.Now().UTC().Format(time.RFC3339))
	return err
}

// ---------- Orders / Shipments ----------

func (s *Store) GetOrder(ctx context.Context, orderNum string) (*Order, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT order_number, status, total_bani, created_at, updated_at
		FROM orders WHERE order_number=?`, orderNum)
	var o Order
	var status, createdAt, updatedAt string
	if err := row.Scan(&o.OrderNumber, &status, &o.TotalBani, &createdAt, &updatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	o.Status = OrderStatus(status)
	o.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	o.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)

	ships, err := s.listShipments(ctx, orderNum)
	if err != nil {
		return nil, err
	}
	o.Shipments = ships
	o.applyAggregates()
	return &o, nil
}

func (s *Store) listShipments(ctx context.Context, orderNum string) ([]Shipment, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, order_number, group_index,
		       COALESCE(seller_group,''), COALESCE(delivery_by,''), delivered_by_emag,
		       COALESCE(easybox_name,''), COALESCE(easybox_address,''),
		       lat, lon, pickup_deadline,
		       COALESCE(pin_code,''), COALESCE(qr_url,''), COALESCE(courier_label,''),
		       total_bani, picked_up_at, dismissed_at
		FROM shipments WHERE order_number=? ORDER BY group_index`, orderNum)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Shipment
	for rows.Next() {
		var sh Shipment
		var deliveredByEmag int
		var lat, lon sql.NullFloat64
		var deadline, pickedUp, dismissed sql.NullString
		if err := rows.Scan(&sh.ID, &sh.OrderNumber, &sh.GroupIndex,
			&sh.SellerGroup, &sh.DeliveryBy, &deliveredByEmag,
			&sh.EasyboxName, &sh.EasyboxAddress,
			&lat, &lon, &deadline,
			&sh.PinCode, &sh.QRURL, &sh.CourierLabel,
			&sh.TotalBani, &pickedUp, &dismissed); err != nil {
			return nil, err
		}
		sh.DeliveredByEmag = deliveredByEmag != 0
		if lat.Valid {
			v := lat.Float64
			sh.Lat = &v
		}
		if lon.Valid {
			v := lon.Float64
			sh.Lon = &v
		}
		if deadline.Valid && deadline.String != "" {
			t, _ := time.Parse(time.RFC3339, deadline.String)
			sh.PickupDeadline = &t
		}
		if pickedUp.Valid && pickedUp.String != "" {
			t, _ := time.Parse(time.RFC3339, pickedUp.String)
			sh.PickedUpAt = &t
		}
		if dismissed.Valid && dismissed.String != "" {
			t, _ := time.Parse(time.RFC3339, dismissed.String)
			sh.DismissedAt = &t
		}
		out = append(out, sh)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		prods, err := s.getProductsByShipment(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Products = prods
	}
	return out, nil
}

func (s *Store) getProductsByShipment(ctx context.Context, shipmentID int64) ([]Product, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT name, COALESCE(image_url,''), qty, line_total_bani, COALESCE(seller_group,'')
		FROM products WHERE shipment_id=? ORDER BY id`, shipmentID)
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

// applyAggregates fills the UI-convenience fields on the order from its
// active shipments. The "active" ordering is: ready-for-pickup > shipped >
// registered; earliest deadline first. The first active shipment supplies
// EasyboxName/Address/Deadline/Lat/Lon.
func (o *Order) applyAggregates() {
	activeTotal := int64(0)
	activeCount := 0
	bestScore := -1
	var best *Shipment
	for i := range o.Shipments {
		sh := &o.Shipments[i]
		if !sh.Active() {
			continue
		}
		activeCount++
		activeTotal += sh.TotalBani
		score := 0
		if sh.HasPickup() {
			score = 2
		} else if sh.EasyboxName != "" {
			score = 1
		}
		if best == nil || score > bestScore ||
			(score == bestScore && sh.PickupDeadline != nil &&
				(best.PickupDeadline == nil || sh.PickupDeadline.Before(*best.PickupDeadline))) {
			best = sh
			bestScore = score
		}
	}
	if activeCount > 0 {
		o.TotalBani = activeTotal
	}
	if best != nil {
		o.EasyboxName = best.EasyboxName
		o.EasyboxAddress = best.EasyboxAddress
		o.PickupDeadline = best.PickupDeadline
		o.Lat = best.Lat
		o.Lon = best.Lon
	}
	o.Status = deriveStatus(o.Status, o.Shipments)
}

// deriveStatus picks the best status across active shipments. Arrived wins
// over shipped wins over registered. Falls back to the order's base status
// if no shipment overrides.
func deriveStatus(base OrderStatus, shipments []Shipment) OrderStatus {
	best := base
	for _, sh := range shipments {
		if !sh.Active() {
			continue
		}
		if sh.HasPickup() {
			if statusRank(StatusArrived) > statusRank(best) {
				best = StatusArrived
			}
		}
	}
	return best
}

// ListActiveOrders returns every order that still has at least one active
// (non-dismissed, non-picked-up) shipment, ordered by derived status and
// earliest deadline.
func (s *Store) ListActiveOrders(ctx context.Context) ([]*Order, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT o.order_number
		FROM orders o
		JOIN shipments sh ON sh.order_number = o.order_number
		WHERE sh.picked_up_at IS NULL AND sh.dismissed_at IS NULL
		ORDER BY o.updated_at DESC`)
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
	// Stable sort: GataDeRidicare first, then InLivrare, then Inregistrata;
	// within a tier, earliest pickup deadline first.
	sortOrders(out)
	return out, nil
}

func sortOrders(os []*Order) {
	// small, so bubble-sort keeps it readable
	for i := 0; i < len(os); i++ {
		for j := i + 1; j < len(os); j++ {
			if orderLess(os[j], os[i]) {
				os[i], os[j] = os[j], os[i]
			}
		}
	}
}

func orderLess(a, b *Order) bool {
	ra, rb := statusRank(a.Status), statusRank(b.Status)
	if ra != rb {
		return ra > rb // higher rank first
	}
	if a.PickupDeadline != nil && b.PickupDeadline != nil {
		return a.PickupDeadline.Before(*b.PickupDeadline)
	}
	if a.PickupDeadline != nil {
		return true
	}
	if b.PickupDeadline != nil {
		return false
	}
	return a.UpdatedAt.After(b.UpdatedAt)
}

// UpsertFromEmail applies an email-derived update. Confirmation/shipped
// emails rebuild the shipments and their product lists. Arrival emails
// attach PIN/QR/deadline to the first matching active shipment (by easybox
// name) that doesn't already have one.
func (s *Store) UpsertFromEmail(ctx context.Context, kind string, p *ParsedEmail) error {
	now := time.Now().UTC().Format(time.RFC3339)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var existingStatus string
	err = tx.QueryRowContext(ctx, `SELECT status FROM orders WHERE order_number=?`, p.OrderNumber).Scan(&existingStatus)
	isNew := errors.Is(err, sql.ErrNoRows)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
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

	finalStatus := newStatus
	if !isNew && statusRank(OrderStatus(existingStatus)) > statusRank(newStatus) {
		finalStatus = OrderStatus(existingStatus)
	}

	if isNew {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO orders(order_number, status, total_bani, created_at, updated_at)
			VALUES(?, ?, ?, ?, ?)`,
			p.OrderNumber, string(finalStatus), p.TotalBani, now, now); err != nil {
			return err
		}
	} else {
		if _, err := tx.ExecContext(ctx, `UPDATE orders SET status=?, updated_at=? WHERE order_number=?`,
			string(finalStatus), now, p.OrderNumber); err != nil {
			return err
		}
	}
	if p.TotalBani > 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE orders SET total_bani=?, updated_at=? WHERE order_number=?`,
			p.TotalBani, now, p.OrderNumber); err != nil {
			return err
		}
	}

	switch kind {
	case "confirmation", "shipped":
		if err := upsertShipmentsTx(ctx, tx, p, kind, now); err != nil {
			return err
		}
	case "arrived":
		if err := attachArrivalTx(ctx, tx, p, now); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// upsertShipmentsTx writes/updates the shipments and their products.
//
// Products are authoritative from the "confirmation" email (full overwrite).
// "shipped" emails only insert products if the shipment has none yet, so a
// later shipped email can never destroy the richer product list (with
// images, full names) that the confirmation email originally provided.
func upsertShipmentsTx(ctx context.Context, tx *sql.Tx, p *ParsedEmail, kind, now string) error {
	for _, sh := range p.Shipments {
		var id int64
		err := tx.QueryRowContext(ctx, `SELECT id FROM shipments WHERE order_number=? AND group_index=?`,
			p.OrderNumber, sh.GroupIndex).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			res, err := tx.ExecContext(ctx, `
				INSERT INTO shipments(
					order_number, group_index,
					seller_group, delivery_by, delivered_by_emag,
					easybox_name, total_bani,
					created_at, updated_at)
				VALUES(?,?,?,?,?,?,?,?,?)`,
				p.OrderNumber, sh.GroupIndex,
				sh.SellerGroup, sh.DeliveryBy, boolInt(sh.DeliveredByEmag),
				nullify(sh.EasyboxName), sh.TotalBani,
				now, now)
			if err != nil {
				return err
			}
			id, err = res.LastInsertId()
			if err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else {
			if _, err := tx.ExecContext(ctx, `
				UPDATE shipments
				SET seller_group = CASE WHEN ? <> '' THEN ? ELSE seller_group END,
				    delivery_by  = CASE WHEN ? <> '' THEN ? ELSE delivery_by END,
				    delivered_by_emag = ?,
				    easybox_name = CASE WHEN ? <> '' THEN ? ELSE easybox_name END,
				    total_bani = CASE WHEN ? > 0 THEN ? ELSE total_bani END,
				    updated_at = ?
				WHERE id = ?`,
				sh.SellerGroup, sh.SellerGroup,
				sh.DeliveryBy, sh.DeliveryBy,
				boolInt(sh.DeliveredByEmag),
				sh.EasyboxName, sh.EasyboxName,
				sh.TotalBani, sh.TotalBani,
				now, id); err != nil {
				return err
			}
		}

		if len(sh.Products) > 0 {
			// Confirmation: authoritative — overwrite. Shipped: only fill
			// in if the shipment has no products yet (so a later shipped
			// email can't destroy confirmation's richer data).
			writeProducts := kind == "confirmation"
			if !writeProducts {
				var existing int
				if err := tx.QueryRowContext(ctx,
					`SELECT COUNT(*) FROM products WHERE shipment_id = ?`, id).Scan(&existing); err != nil {
					return err
				}
				writeProducts = existing == 0
			}
			if writeProducts {
				if _, err := tx.ExecContext(ctx, `DELETE FROM products WHERE shipment_id = ?`, id); err != nil {
					return err
				}
				for _, pr := range sh.Products {
					if _, err := tx.ExecContext(ctx, `
						INSERT INTO products(shipment_id, order_number, name, image_url, qty, line_total_bani, seller_group)
						VALUES(?,?,?,?,?,?,?)`,
						id, p.OrderNumber, pr.Name, pr.ImageURL, pr.Qty, pr.LineTotalBani, pr.SellerGroup); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

// attachArrivalTx assigns PIN/QR/deadline to the best matching shipment for
// an arrival/reminder email. Preference order:
//  1. Non-dismissed, non-picked-up, no PIN yet, matching easybox name.
//  2. Non-dismissed, non-picked-up, already has a PIN that matches (reminder
//     refresh).
//  3. Non-dismissed, non-picked-up (easybox unknown / names don't match).
//
// If nothing matches we silently drop the arrival — the user likely dismissed
// everything they didn't want.
func attachArrivalTx(ctx context.Context, tx *sql.Tx, p *ParsedEmail, now string) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, COALESCE(easybox_name,''), COALESCE(pin_code,''),
		       picked_up_at, dismissed_at
		FROM shipments
		WHERE order_number=?
		ORDER BY group_index`, p.OrderNumber)
	if err != nil {
		return err
	}
	type cand struct {
		id       int64
		easybox  string
		pin      string
		pickedUp bool
		dismiss  bool
	}
	var cands []cand
	for rows.Next() {
		var c cand
		var pickedUp, dismissed sql.NullString
		if err := rows.Scan(&c.id, &c.easybox, &c.pin, &pickedUp, &dismissed); err != nil {
			rows.Close()
			return err
		}
		c.pickedUp = pickedUp.Valid && pickedUp.String != ""
		c.dismiss = dismissed.Valid && dismissed.String != ""
		cands = append(cands, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	target := int64(-1)
	pinFromURL := pinFromQRURL(p.QRURL)

	// 2. PIN match: reminder for a shipment we already set up.
	if pinFromURL != "" {
		for _, c := range cands {
			if c.pickedUp || c.dismiss {
				continue
			}
			if strings.EqualFold(c.pin, pinFromURL) {
				target = c.id
				break
			}
		}
	}
	// 1. Easybox match + no PIN yet.
	if target < 0 && p.ArrivalEasybox != "" {
		for _, c := range cands {
			if c.pickedUp || c.dismiss {
				continue
			}
			if c.pin != "" {
				continue
			}
			if easyboxesMatch(c.easybox, p.ArrivalEasybox) {
				target = c.id
				break
			}
		}
	}
	// 3. Fallback: any active shipment with no PIN yet.
	if target < 0 {
		for _, c := range cands {
			if c.pickedUp || c.dismiss {
				continue
			}
			if c.pin == "" {
				target = c.id
				break
			}
		}
	}
	if target < 0 {
		return nil
	}

	var deadline sql.NullString
	if p.PickupDeadline != nil {
		deadline = sql.NullString{String: p.PickupDeadline.UTC().Format(time.RFC3339), Valid: true}
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE shipments
		SET easybox_name    = CASE WHEN ? <> '' THEN ? ELSE easybox_name END,
		    easybox_address = CASE WHEN ? <> '' THEN ? ELSE easybox_address END,
		    pickup_deadline = COALESCE(?, pickup_deadline),
		    pin_code        = CASE WHEN ? <> '' THEN ? ELSE pin_code END,
		    qr_url          = CASE WHEN ? <> '' THEN ? ELSE qr_url END,
		    courier_label   = CASE WHEN ? <> '' THEN ? ELSE courier_label END,
		    updated_at      = ?
		WHERE id = ?`,
		p.ArrivalEasybox, p.ArrivalEasybox,
		p.ArrivalEasyboxAddr, p.ArrivalEasyboxAddr,
		deadline,
		p.PinCode, p.PinCode,
		p.QRURL, p.QRURL,
		p.ArrivalCourier, p.ArrivalCourier,
		now, target)
	return err
}

func pinFromQRURL(u string) string {
	if u == "" {
		return ""
	}
	if m := reQRURL.FindStringSubmatch(u); m != nil {
		return strings.ToUpper(m[2])
	}
	return ""
}

func easyboxesMatch(a, b string) bool {
	return normalize(strings.TrimSpace(a)) == normalize(strings.TrimSpace(b)) && a != ""
}

// ---------- Dismiss / pickup (per-shipment) ----------

func (s *Store) DismissShipment(ctx context.Context, orderNum string, shipmentID int64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE shipments SET dismissed_at=?, updated_at=?
		WHERE id=? AND order_number=?`,
		time.Now().UTC().Format(time.RFC3339),
		time.Now().UTC().Format(time.RFC3339),
		shipmentID, orderNum)
	return err
}

func (s *Store) RestoreShipment(ctx context.Context, orderNum string, shipmentID int64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE shipments SET dismissed_at=NULL, updated_at=?
		WHERE id=? AND order_number=?`,
		time.Now().UTC().Format(time.RFC3339),
		shipmentID, orderNum)
	return err
}

func (s *Store) MarkShipmentPickedUp(ctx context.Context, orderNum string, shipmentID int64) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx, `
		UPDATE shipments SET picked_up_at=?, updated_at=?
		WHERE id=? AND order_number=?`,
		now, now, shipmentID, orderNum)
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

// AddressesWithoutCoords returns distinct easybox addresses of active
// shipments lacking lat/lon.
func (s *Store) AddressesWithoutCoords(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT easybox_address FROM shipments
		WHERE picked_up_at IS NULL AND dismissed_at IS NULL
		  AND easybox_address IS NOT NULL AND easybox_address <> ''
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

// ApplyCoordsToAddress sets lat/lon on every active shipment matching the
// given address.
func (s *Store) ApplyCoordsToAddress(ctx context.Context, address string, lat, lon float64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE shipments SET lat=?, lon=?, updated_at=?
		WHERE easybox_address=? AND picked_up_at IS NULL AND dismissed_at IS NULL`,
		lat, lon, time.Now().UTC().Format(time.RFC3339), address)
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullify(s string) any {
	if s == "" {
		return nil
	}
	return s
}
