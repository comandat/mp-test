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

// Start performs login / session restore, then polls the inbox periodically.
// Blocks until ctx is cancelled.
func (p *ProtonSync) Start(ctx context.Context) error {
	if err := p.connect(ctx); err != nil {
		return fmt.Errorf("proton connect: %w", err)
	}
	log.Printf("proton: connected")

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

	// Proton's edge rejects requests with the library's default app-version
	// header. Present a Bridge-style identifier by default; allow override via
	// PROTON_APP_VERSION in case Proton tightens its whitelist later.
	appVersion := p.cfg.ProtonAppVersion
	if appVersion == "" {
		appVersion = "Other"
	}
	opts := []proton.Option{proton.WithAppVersion(appVersion)}
	if p.cfg.ProtonDebug {
		opts = append(opts, proton.WithDebug(true))
	}
	p.mgr = proton.New(opts...)

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

	// Persist rotated tokens on every refresh.
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

// scanInbox lists recent Inbox messages matching a handful of subject hints,
// classifies each new one, and updates the store.
func (p *ProtonSync) scanInbox(ctx context.Context) error {
	p.mu.Lock()
	c := p.client
	p.mu.Unlock()
	if c == nil {
		return fmt.Errorf("no client")
	}

	// Subject hints cover both eMAG-sent emails (which always include
	// "comandă"/"predată") and Sameday-sent emails (titled "Notificare NN").
	subjectHints := []string{"comand", "ajuns", "easybox", "predat", "notificare"}
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
	textForClassify := plainBody
	if textForClassify == "" {
		textForClassify = htmlToText(htmlBody)
	}

	kind := ClassifyEmail(meta.Subject, textForClassify)
	if kind == "" {
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

	if err := p.store.UpsertFromEmail(ctx, kind, parsed); err != nil {
		return fmt.Errorf("upsert: %w", err)
	}
	log.Printf("proton: %s order=%s products=%d", kind, parsed.OrderNumber, len(parsed.Products))
	return p.store.MarkProcessed(ctx, meta.ID, kind, parsed.OrderNumber)
}

// extractMIMEBodies walks MIME bytes and returns the HTML + plain-text parts.
func extractMIMEBodies(mimeBytes []byte) (htmlBody, plainBody string) {
	msg, err := mail.ReadMessage(strings.NewReader(string(mimeBytes)))
	if err != nil {
		return "", string(mimeBytes)
	}
	return walkMIMEPart(msg.Header.Get("Content-Type"), msg.Body, msg.Header.Get("Content-Transfer-Encoding"))
}

func walkMIMEPart(contentType string, body io.Reader, topEncoding string) (htmlOut, plainOut string) {
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
	data = decodeTransfer(data, strings.ToLower(topEncoding))
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
		p = trimCRLF(p)
		idx := indexOfDoubleNewline(p)
		if idx < 0 {
			continue
		}
		headerBlock := string(p[:idx])
		body := trimLeadingNewlines(p[idx:])

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
			htmlOut = string(body)
		} else if mt == "text/plain" && plainOut == "" {
			plainOut = string(body)
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
	// Minimal inline QP decoder (handles =XX and soft line breaks "=\r\n").
	var out []byte
	i := 0
	for i < len(body) {
		b := body[i]
		if b == '=' && i+1 < len(body) {
			nx := body[i+1]
			if nx == '\n' {
				i += 2
				continue
			}
			if nx == '\r' && i+2 < len(body) && body[i+2] == '\n' {
				i += 3
				continue
			}
			if i+2 < len(body) {
				hi := hexVal(body[i+1])
				lo := hexVal(body[i+2])
				if hi >= 0 && lo >= 0 {
					out = append(out, byte(hi<<4|lo))
					i += 3
					continue
				}
			}
		}
		out = append(out, b)
		i++
	}
	return out
}

func hexVal(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
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
