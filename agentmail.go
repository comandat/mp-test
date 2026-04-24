package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// AgentMailSync polls an AgentMail inbox over the REST API, classifies
// each new message, and updates the store. It replaces the old Proton
// integration — all mail now lands in an AgentMail inbox the user owns.
type AgentMailSync struct {
	cfg   *Config
	store *Store
	http  *http.Client
}

func NewAgentMailSync(cfg *Config, store *Store) *AgentMailSync {
	return &AgentMailSync{
		cfg:   cfg,
		store: store,
		http:  &http.Client{Timeout: 30 * time.Second},
	}
}

// amMessage mirrors the handful of fields we consume from AgentMail's
// message list / get endpoints. The API returns more than this; we only
// declare what we read.
type amMessage struct {
	MessageID string   `json:"message_id"`
	ThreadID  string   `json:"thread_id"`
	Labels    []string `json:"labels"`
	Timestamp string   `json:"timestamp"`
	From      string   `json:"from"`
	To        []string `json:"to"`
	Subject   string   `json:"subject"`
	Preview   string   `json:"preview"`
	Text      string   `json:"text"`
	HTML      string   `json:"html"`
}

type amListResponse struct {
	Count         int         `json:"count"`
	NextPageToken string      `json:"next_page_token"`
	Messages      []amMessage `json:"messages"`
}

// Start runs an initial scan and then polls at SyncIntervalSeconds.
// Blocks until ctx is cancelled.
func (a *AgentMailSync) Start(ctx context.Context) error {
	if a.cfg.AgentMailAPIKey == "" || a.cfg.AgentMailInboxID == "" {
		return fmt.Errorf("AGENTMAIL_API_KEY and AGENTMAIL_INBOX_ID are required")
	}
	log.Printf("agentmail: polling inbox %q every %ds", a.cfg.AgentMailInboxID, a.cfg.SyncIntervalSeconds)

	if err := a.scan(ctx); err != nil {
		log.Printf("agentmail: initial scan: %v", err)
	}

	ticker := time.NewTicker(time.Duration(a.cfg.SyncIntervalSeconds) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := a.scan(ctx); err != nil {
				log.Printf("agentmail: scan: %v", err)
			}
		}
	}
}

// scan walks every page of the inbox (oldest-first), skipping anything
// already in emails_processed, and processes new messages.
func (a *AgentMailSync) scan(ctx context.Context) error {
	pageToken := ""
	checked := 0
	processed := 0
	for {
		resp, err := a.listMessages(ctx, pageToken)
		if err != nil {
			return err
		}
		checked += len(resp.Messages)
		for _, m := range resp.Messages {
			// Skip messages we sent ourselves. AgentMail labels received
			// messages with "received"; drafts/sends get "sent" etc.
			if !hasLabel(m.Labels, "received") {
				continue
			}
			already, err := a.store.IsProcessed(ctx, m.MessageID)
			if err != nil {
				log.Printf("agentmail: isProcessed: %v", err)
				continue
			}
			if already {
				continue
			}
			if err := a.processMessage(ctx, m); err != nil {
				log.Printf("agentmail: process %s (%q): %v", m.MessageID, m.Subject, err)
			} else {
				processed++
			}
		}
		if resp.NextPageToken == "" {
			break
		}
		pageToken = resp.NextPageToken
	}
	log.Printf("agentmail: scan — %d messages checked, %d new processed", checked, processed)
	return nil
}

func (a *AgentMailSync) listMessages(ctx context.Context, pageToken string) (*amListResponse, error) {
	q := url.Values{}
	q.Set("ascending", "true")
	q.Set("limit", "100")
	if pageToken != "" {
		q.Set("page_token", pageToken)
	}
	u := fmt.Sprintf("%s/v0/inboxes/%s/messages?%s",
		strings.TrimRight(a.cfg.AgentMailBaseURL, "/"),
		url.PathEscape(a.cfg.AgentMailInboxID),
		q.Encode())

	var out amListResponse
	if err := a.doJSON(ctx, u, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (a *AgentMailSync) getMessage(ctx context.Context, messageID string) (*amMessage, error) {
	u := fmt.Sprintf("%s/v0/inboxes/%s/messages/%s",
		strings.TrimRight(a.cfg.AgentMailBaseURL, "/"),
		url.PathEscape(a.cfg.AgentMailInboxID),
		url.PathEscape(messageID))

	var out amMessage
	if err := a.doJSON(ctx, u, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// processMessage fetches the full body, classifies, parses, and upserts.
// Unrecognized messages are still recorded in emails_processed so we do
// not re-fetch them on every poll.
func (a *AgentMailSync) processMessage(ctx context.Context, meta amMessage) error {
	full, err := a.getMessage(ctx, meta.MessageID)
	if err != nil {
		return fmt.Errorf("get: %w", err)
	}
	htmlBody := full.HTML
	plainBody := full.Text
	if plainBody == "" {
		plainBody = htmlToText(htmlBody)
	}

	kind := ClassifyEmail(full.Subject, plainBody)
	if kind == "" {
		return a.store.MarkProcessed(ctx, meta.MessageID, "", "")
	}

	var parsed *ParsedEmail
	switch kind {
	case "confirmation":
		parsed, err = ParseConfirmation(full.Subject, htmlBody)
	case "shipped":
		parsed, err = ParseShipped(full.Subject, htmlBody)
	case "arrived":
		parsed, err = ParseArrived(htmlBody, plainBody)
	}
	if err != nil {
		return fmt.Errorf("parse %s: %w", kind, err)
	}
	if parsed == nil {
		return nil
	}

	if err := a.store.UpsertFromEmail(ctx, kind, parsed); err != nil {
		return fmt.Errorf("upsert: %w", err)
	}
	log.Printf("agentmail: %s order=%s shipments=%d", kind, parsed.OrderNumber, len(parsed.Shipments))
	return a.store.MarkProcessed(ctx, meta.MessageID, kind, parsed.OrderNumber)
}

func (a *AgentMailSync) doJSON(ctx context.Context, u string, out any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.cfg.AgentMailAPIKey)
	req.Header.Set("Accept", "application/json")
	res, err := a.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		body, _ := io.ReadAll(res.Body)
		return fmt.Errorf("status %d: %s", res.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(res.Body).Decode(out)
}

func hasLabel(labels []string, needle string) bool {
	for _, l := range labels {
		if strings.EqualFold(l, needle) {
			return true
		}
	}
	return false
}
