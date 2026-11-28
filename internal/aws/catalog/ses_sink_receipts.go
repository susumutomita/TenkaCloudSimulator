package catalog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// SESReceipt describes the regional local sink; it is not an AWS event type.
type SESReceipt struct {
	MessageID    string `json:"messageId"`
	Sender       string `json:"from"`
	Recipient    string `json:"to"`
	Template     string `json:"template,omitempty"`
	AcceptedAt   string `json:"acceptedAt"`
	Outcome      string `json:"outcome"`
	Reason       string `json:"reason,omitempty"`
	ContentBytes int    `json:"contentBytes"`
}

const sesReceiptColumns = `SELECT m.id,m.sender,m.recipient,m.template,m.accepted_at,m.outcome,m.reason,i.id IS NOT NULL,coalesce(length(i.mime),0)
 FROM aws_ses_sink_messages m LEFT JOIN aws_ses_local_inbox i ON i.id=m.id`

func readSESReceipt(scanner interface{ Scan(...any) error }) (SESReceipt, error) {
	var r SESReceipt
	var hasContent bool
	err := scanner.Scan(&r.MessageID, &r.Sender, &r.Recipient, &r.Template, &r.AcceptedAt, &r.Outcome, &r.Reason, &hasContent, &r.ContentBytes)
	if err != nil {
		return r, err
	}
	_, fromOK := sesAddress(r.Sender)
	domain, toOK := sesAddress(r.Recipient)
	_, timeErr := time.Parse(time.RFC3339Nano, r.AcceptedAt)
	delivered := r.Outcome == "delivered" && hasContent && r.Reason == "" && r.Recipient == localSESMailbox && r.ContentBytes > 0 && r.ContentBytes <= maxSESMIME
	rejected := r.Outcome == "rejected" && !hasContent && r.Reason == "mailbox-not-found" && r.Recipient != localSESMailbox && r.ContentBytes == 0
	if !sesSelector.MatchString(r.MessageID) || !fromOK || !toOK || domain != "inbox.ses.test" || timeErr != nil || r.Template != "" && !sesTemplateName.MatchString(r.Template) || !(delivered || rejected) {
		return r, errors.New("invalid saved SES local sink receipt")
	}
	return r, nil
}

func (e *Engine) SESSinkList(ctx context.Context, region string) ([]SESReceipt, error) {
	rows, err := e.store.db.QueryContext(ctx, sesReceiptColumns+" WHERE m.region=? ORDER BY m.seq DESC LIMIT 101", region)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	receipts := []SESReceipt{}
	for rows.Next() {
		r, err := readSESReceipt(rows)
		if err != nil {
			return nil, err
		}
		if len(receipts) == 100 {
			return nil, errors.New("invalid saved SES receipt count")
		}
		receipts = append(receipts, r)
	}
	return receipts, rows.Err()
}

func (e *Engine) SESSinkMessage(ctx context.Context, region, id string, content, remove bool) (SESReceipt, []byte, error) {
	var receipt SESReceipt
	tx, err := e.store.db.BeginTx(ctx, nil)
	if err != nil {
		return receipt, nil, err
	}
	defer tx.Rollback()
	receipt, err = readSESReceipt(tx.QueryRowContext(ctx, sesReceiptColumns+" WHERE m.region=? AND m.id=?", region, id))
	if err != nil {
		return receipt, nil, err
	}
	var data []byte
	if content {
		if receipt.Outcome != "delivered" {
			return receipt, nil, sql.ErrNoRows
		}
		var storedDigest []byte
		if err = tx.QueryRowContext(ctx, "SELECT mime,sha256 FROM aws_ses_local_inbox WHERE id=?", id).Scan(&data, &storedDigest); err != nil {
			return receipt, nil, err
		}
		digest := sha256.Sum256(data)
		if !bytes.Equal(digest[:], storedDigest) || len(data) != receipt.ContentBytes || !strings.HasPrefix(string(data), "From: "+receipt.Sender+"\r\nTo: "+receipt.Recipient+"\r\n") {
			return receipt, nil, errors.New("invalid saved SES local MIME")
		}
	}
	if remove {
		if err = exactlyOne(tx.ExecContext(ctx, "DELETE FROM aws_ses_sink_messages WHERE region=? AND id=?", region, id)); err != nil {
			return receipt, nil, err
		}
		var present bool
		err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM aws_ses_sink_messages WHERE id=?) OR EXISTS(SELECT 1 FROM aws_ses_local_inbox WHERE id=?)", id, id).Scan(&present)
		if err != nil {
			return receipt, nil, err
		}
		if present {
			return receipt, nil, errors.New("local SES receipt cleanup did not remove its owned state")
		}
	}
	if err = tx.Commit(); err != nil {
		return receipt, nil, err
	}
	return receipt, data, nil
}
