package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"mime"
	"net/mail"
	"strings"
	"sync"
	"time"

	"github.com/ProtonMail/go-proton-api"
	"github.com/ProtonMail/gopenpgp/v2/crypto"
	"github.com/google/uuid"
	"github.com/pquerna/otp/totp"
)

type ProtonSync struct {
	cfg   *Config
	store *Store

	mu     sync.Mutex
	mgr    *proton.Manager
	client *proton.Client
	addrKR map[string]*crypto.KeyRing
}

func NewProtonSync(cfg *Config, store *Store) *ProtonSync {
	return &ProtonSync{cfg: cfg, store: store}
}

// Start performs login / session restore, then enters a polling loop.
// Blocks until ctx is cancelled.
func (p *ProtonSync) Start(ctx context.Context) error {
	if err := p.connect(ctx); err != nil {
		return fmt.Errorf("proton connect: %w", err)
	}
	log.Printf("proton: connected")

	// Initial backfill then periodic poll.
	if err := p.scanInbox(ctx); err != nil {
		log.Printf("proton: initial scan error: %v", err)
	}

	interval := time.Duration(p.cfg.SyncIntervalSeconds) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := p.scanInbox(ctx); err != nil {
				log.Printf("proton: scan error: %v", err)
				// On auth errors, try reconnecting once.
				if strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "unauth") {
					if err := p.connect(ctx); err != nil {
						log.Printf("proton: reconnect failed: %v", err)
					}
				}
			}
		}
	}
}

func (p *ProtonSync) connect(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.client != nil {
		p.client.Close()
		p.client = nil
	}

	p.mgr = proton.New()

	sess, err := p.store.LoadSession(ctx)
	if err != nil {
		return fmt.Errorf("load session: %w", err)
	}

	var c *proton.Client
	if sess != nil && sess.UID != "" && sess.RefreshToken != "" {
		c, _, err = p.mgr.NewClientWithRefresh(ctx, sess.UID, sess.RefreshToken)
		if err != nil {
			log.Printf("proton: refresh failed (%v) — falling back to fresh login", err)
			c = nil
		}
	}
	if c == nil {
		log.Printf("proton: performing fresh login for %s", p.cfg.ProtonUsername)
		newC, auth, err := p.mgr.NewClientWithLogin(ctx, p.cfg.ProtonUsername, []byte(p.cfg.ProtonPassword))
		if err != nil {
			return fmt.Errorf("login: %w", err)
		}
		if auth.TwoFA.Enabled&proton.HasTOTP != 0 {
			if p.cfg.ProtonTOTPSecret == "" {
				newC.Close()
				return fmt.Errorf("account requires TOTP but PROTON_TOTP_SECRET is not set")
			}
			code, err := totp.GenerateCode(p.cfg.ProtonTOTPSecret, time.Now())
			if err != nil {
				newC.Close()
				return fmt.Errorf("generate TOTP: %w", err)
			}
			if err := newC.Auth2FA(ctx, proton.Auth2FAReq{TwoFactorCode: code}); err != nil {
				newC.Close()
				return fmt.Errorf("2FA: %w", err)
			}
		}
		if err := p.store.SaveSessionAuth(ctx, auth.UID, auth.AccessToken, auth.RefreshToken); err != nil {
			log.Printf("proton: save session: %v", err)
		}
		c = newC
	}

	// Persist rotated tokens.
	c.AddAuthHandler(func(a proton.Auth) {
		bg, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := p.store.SaveSessionAuth(bg, a.UID, a.AccessToken, a.RefreshToken); err != nil {
			log.Printf("proton: rotate save: %v", err)
		}
	})

	user, err := c.GetUser(ctx)
	if err != nil {
		return fmt.Errorf("get user: %w", err)
	}
	addrs, err := c.GetAddresses(ctx)
	if err != nil {
		return fmt.Errorf("get addresses: %w", err)
	}
	salts, err := c.GetSalts(ctx)
	if err != nil {
		return fmt.Errorf("get salts: %w", err)
	}
	keyPass, err := salts.SaltForKey([]byte(p.cfg.ProtonMailboxPassword), user.Keys.Primary().ID)
	if err != nil {
		return fmt.Errorf("salt for key: %w", err)
	}
	_, addrKR, err := proton.Unlock(user, addrs, keyPass)
	if err != nil {
		return fmt.Errorf("unlock keys: %w (mailbox password wrong?)", err)
	}
	p.client = c
	p.addrKR = addrKR
	return nil
}

// scanInbox fetches recent Inbox messages, classifies and processes unknown ones.
// We use multiple subject queries to catch Romanian eMAG emails.
func (p *ProtonSync) scanInbox(ctx context.Context) error {
	p.mu.Lock()
	c := p.client
	p.mu.Unlock()
	if c == nil {
		return fmt.Errorf("no client")
	}

	subjectHints := []string{"comand", "ajuns", "easybox", "predat"}
	seen := map[string]proton.MessageMetadata{}

	for _, hint := range subjectHints {
		metas, err := c.GetMessageMetadata(ctx, proton.MessageFilter{
			LabelID: proton.InboxLabel,
			Subject: hint,
		})
		if err != nil {
			return fmt.Errorf("list subject=%q: %w", hint, err)
		}
		for _, m := range metas {
			seen[m.ID] = m
		}
	}

	log.Printf("proton: scan found %d candidate messages", len(seen))

	for id, meta := range seen {
		already, err := p.store.IsProcessed(ctx, id)
		if err != nil {
			log.Printf("proton: isProcessed: %v", err)
			continue
		}
		if already {
			continue
		}
		if err := p.processMessage(ctx, meta); err != nil {
			log.Printf("proton: process %s (%q): %v", id, meta.Subject, err)
		}
	}
	return nil
}

func (p *ProtonSync) processMessage(ctx context.Context, meta proton.MessageMetadata) error {
	c := p.client
	full, err := c.GetMessage(ctx, meta.ID)
	if err != nil {
		return fmt.Errorf("get message: %w", err)
	}
	kr := p.addrKR[full.AddressID]
	if kr == nil {
		return fmt.Errorf("no keyring for address %s", full.AddressID)
	}
	mimeBytes, err := full.Decrypt(kr)
	if err != nil {
		return fmt.Errorf("decrypt: %w", err)
	}

	htmlBody, plainBody := extractMIMEBodies(mimeBytes)
	if htmlBody == "" && plainBody == "" {
		return fmt.Errorf("empty body")
	}
	textForClassify := plainBody
	if textForClassify == "" {
		textForClassify = htmlToText(htmlBody)
	}

	kind := ClassifyEmail(meta.Subject, textForClassify)
	if kind == "" {
		// Not interesting — record as processed so we don't re-scan.
		_ = p.store.MarkProcessed(ctx, meta.ID, "", "")
		return nil
	}

	var parsed *ParsedEmail
	switch kind {
	case "confirmation":
		parsed, err = ParseConfirmation(meta.Subject, htmlBody)
	case "shipped":
		parsed, err = ParseShipped(meta.Subject, htmlBody)
	case "arrived":
		parsed, err = ParseArrived(htmlBody, textForClassify)
	}
	if err != nil {
		return fmt.Errorf("parse %s: %w", kind, err)
	}
	if parsed == nil {
		return nil
	}

	// For arrived: fetch and persist the QR image.
	if kind == "arrived" && parsed.QRContentID != "" {
		for _, att := range full.Attachments {
			if matchesCID(att, parsed.QRContentID) {
				data, err := fetchAndDecryptAttachment(ctx, c, kr, att)
				if err != nil {
					log.Printf("proton: qr attachment decrypt: %v", err)
					break
				}
				attID := uuid.NewString()
				ct := string(att.MIMEType)
				if ct == "" {
					ct = "image/png"
				}
				if err := p.store.SaveAttachment(ctx, attID, parsed.OrderNumber, ct, data); err != nil {
					log.Printf("proton: save attachment: %v", err)
					break
				}
				parsed.QRAttachmentID = attID
				break
			}
		}
	}

	if err := p.store.UpsertFromEmail(ctx, kind, parsed); err != nil {
		return fmt.Errorf("upsert: %w", err)
	}
	log.Printf("proton: %s order=%s products=%d", kind, parsed.OrderNumber, len(parsed.Products))
	return p.store.MarkProcessed(ctx, meta.ID, kind, parsed.OrderNumber)
}

func matchesCID(att proton.Attachment, cid string) bool {
	for _, h := range att.Headers["Content-Id"] {
		if strings.Trim(h, "<> ") == strings.Trim(cid, "<> ") {
			return true
		}
	}
	for _, h := range att.Headers["Content-ID"] {
		if strings.Trim(h, "<> ") == strings.Trim(cid, "<> ") {
			return true
		}
	}
	return false
}

func fetchAndDecryptAttachment(ctx context.Context, c *proton.Client, kr *crypto.KeyRing, att proton.Attachment) ([]byte, error) {
	raw, err := c.GetAttachment(ctx, att.ID)
	if err != nil {
		return nil, err
	}
	kps, err := base64.StdEncoding.DecodeString(att.KeyPackets)
	if err != nil {
		return nil, err
	}
	msg := crypto.NewPGPSplitMessage(kps, raw).GetPGPMessage()
	plain, err := kr.Decrypt(msg, nil, crypto.GetUnixTime())
	if err != nil {
		return nil, err
	}
	return plain.GetBinary(), nil
}

// extractMIMEBodies walks the raw MIME message bytes and returns the best HTML and plain-text parts.
func extractMIMEBodies(mimeBytes []byte) (htmlBody, plainBody string) {
	msg, err := mail.ReadMessage(strings.NewReader(string(mimeBytes)))
	if err != nil {
		return "", string(mimeBytes)
	}
	return walkMIMEPart(msg.Header.Get("Content-Type"), msg.Body)
}

func walkMIMEPart(contentType string, body io.Reader) (htmlOut, plainOut string) {
	mt, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		data, _ := io.ReadAll(body)
		return "", string(data)
	}
	if strings.HasPrefix(mt, "multipart/") {
		boundary := params["boundary"]
		if boundary == "" {
			return "", ""
		}
		data, _ := io.ReadAll(body)
		return walkMultipart(data, boundary)
	}
	data, _ := io.ReadAll(body)
	decoded := string(data)
	if mt == "text/html" {
		return decoded, ""
	}
	return "", decoded
}

func walkMultipart(data []byte, boundary string) (htmlOut, plainOut string) {
	delim := []byte("--" + boundary)
	parts := splitAt(data, delim)
	for _, p := range parts {
		// Each part starts with headers, then blank line, then body.
		p = trimCRLF(p)
		idx := indexOfDoubleNewline(p)
		if idx < 0 {
			continue
		}
		headerBlock := string(p[:idx])
		body := p[idx:]
		body = trimLeadingNewlines(body)

		h := parseHeaders(headerBlock)
		ct := h["content-type"]
		cte := strings.ToLower(h["content-transfer-encoding"])
		body = decodeTransfer(body, cte)

		mt, params, err := mime.ParseMediaType(ct)
		if err != nil {
			continue
		}
		if strings.HasPrefix(mt, "multipart/") {
			nb := params["boundary"]
			if nb == "" {
				continue
			}
			h2, p2 := walkMultipart(body, nb)
			if htmlOut == "" {
				htmlOut = h2
			}
			if plainOut == "" {
				plainOut = p2
			}
			continue
		}
		if mt == "text/html" && htmlOut == "" {
			htmlOut = decodeCharset(body, params["charset"])
		} else if mt == "text/plain" && plainOut == "" {
			plainOut = decodeCharset(body, params["charset"])
		}
	}
	return
}

func decodeTransfer(body []byte, encoding string) []byte {
	switch encoding {
	case "base64":
		clean := strings.Map(func(r rune) rune {
			if r == '\r' || r == '\n' {
				return -1
			}
			return r
		}, string(body))
		dec, err := base64.StdEncoding.DecodeString(clean)
		if err != nil {
			return body
		}
		return dec
	case "quoted-printable":
		return decodeQuotedPrintable(body)
	}
	return body
}

func decodeQuotedPrintable(body []byte) []byte {
	r := strings.NewReader(string(body))
	out, err := io.ReadAll(quotedPrintableReader(r))
	if err != nil {
		return body
	}
	return out
}

func decodeCharset(body []byte, charset string) string {
	// Best effort — most eMAG emails are UTF-8.
	return string(body)
}

func splitAt(data, delim []byte) [][]byte {
	var out [][]byte
	start := 0
	for i := 0; i+len(delim) <= len(data); i++ {
		if bytesEqual(data[i:i+len(delim)], delim) {
			if i > start {
				out = append(out, data[start:i])
			}
			start = i + len(delim)
			i += len(delim) - 1
		}
	}
	if start < len(data) {
		out = append(out, data[start:])
	}
	return out
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func trimCRLF(b []byte) []byte {
	for len(b) > 0 && (b[0] == '\r' || b[0] == '\n') {
		b = b[1:]
	}
	for len(b) > 0 && (b[len(b)-1] == '\r' || b[len(b)-1] == '\n') {
		b = b[:len(b)-1]
	}
	return b
}

func trimLeadingNewlines(b []byte) []byte {
	for len(b) > 0 && (b[0] == '\r' || b[0] == '\n') {
		b = b[1:]
	}
	return b
}

func indexOfDoubleNewline(b []byte) int {
	for i := 0; i+1 < len(b); i++ {
		if b[i] == '\n' && b[i+1] == '\n' {
			return i
		}
		if i+3 < len(b) && b[i] == '\r' && b[i+1] == '\n' && b[i+2] == '\r' && b[i+3] == '\n' {
			return i
		}
	}
	return -1
}

func parseHeaders(block string) map[string]string {
	out := map[string]string{}
	lines := strings.Split(block, "\n")
	var curKey, curVal string
	flush := func() {
		if curKey != "" {
			out[strings.ToLower(curKey)] = strings.TrimSpace(curVal)
		}
		curKey, curVal = "", ""
	}
	for _, l := range lines {
		l = strings.TrimRight(l, "\r")
		if l == "" {
			continue
		}
		if l[0] == ' ' || l[0] == '\t' {
			curVal += " " + strings.TrimSpace(l)
			continue
		}
		flush()
		idx := strings.Index(l, ":")
		if idx < 0 {
			continue
		}
		curKey = strings.TrimSpace(l[:idx])
		curVal = strings.TrimSpace(l[idx+1:])
	}
	flush()
	return out
}

// htmlToText is a best-effort text extractor from HTML for classification purposes.
func htmlToText(h string) string {
	// Strip tags very crudely.
	var b strings.Builder
	skip := false
	for _, r := range h {
		switch r {
		case '<':
			skip = true
		case '>':
			skip = false
			b.WriteRune(' ')
		default:
			if !skip {
				b.WriteRune(r)
			}
		}
	}
	// Collapse whitespace but preserve line breaks approximately.
	s := b.String()
	s = strings.ReplaceAll(s, "\r", "")
	// Convert runs of spaces to single spaces but keep newlines.
	out := make([]rune, 0, len(s))
	var prev rune
	for _, r := range s {
		if r == ' ' && prev == ' ' {
			continue
		}
		out = append(out, r)
		prev = r
	}
	return string(out)
}
