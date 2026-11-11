package aws

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
)

type SQSDeleteMessageBatchEntry struct {
	Id            string
	ReceiptHandle string
}

type SQSDeleteMessageBatchSuccess struct{ Id string }

type SQSDeleteMessageBatchFailure struct {
	Id          string
	Code        string
	Message     string
	SenderFault bool
}

type SQSDeleteMessageBatchResult struct {
	Successful []SQSDeleteMessageBatchSuccess
	Failed     []SQSDeleteMessageBatchFailure
}

// Batch objects use exact field names and reject duplicate keys before Go's
// last-key-wins/case-insensitive struct decoder can choose a different target.
func sqsBatchObject(data []byte, allowed ...string) (map[string]json.RawMessage, error) {
	invalid := sqsFailure{"InvalidParameterValue", "Expected an unambiguous batch request object with supported, non-null fields."}
	d := json.NewDecoder(bytes.NewReader(data))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, invalid
	}
	fields := map[string]json.RawMessage{}
	for d.More() {
		token, err := d.Token()
		name, ok := token.(string)
		if err != nil || !ok || fields[name] != nil {
			return nil, invalid
		}
		supported := false
		for _, field := range allowed {
			if name == field {
				supported = true
			}
		}
		var value json.RawMessage
		if !supported || d.Decode(&value) != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, invalid
		}
		fields[name] = value
	}
	if token, err = d.Token(); err != nil || token != json.Delim('}') {
		return nil, invalid
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, invalid
	}
	return fields, nil
}

func validateSQSDeleteBatchJSON(data []byte) error {
	raw, err := sqsBatchObject(data, "QueueUrl", "Entries")
	if err != nil {
		return err
	}
	var queueURL string
	if !validSQSJSONString(raw["QueueUrl"]) || json.Unmarshal(raw["QueueUrl"], &queueURL) != nil || !validQueueName(queueNameFromURL(queueURL)) {
		return sqsFailure{"InvalidParameterValue", "QueueUrl must identify a supported Standard queue."}
	}
	var rawEntries []json.RawMessage
	if json.Unmarshal(raw["Entries"], &rawEntries) != nil {
		return sqsFailure{"InvalidParameterValue", "Entries is required and must be an array."}
	}
	entries := make([]SQSDeleteMessageBatchEntry, len(rawEntries))
	for i, data := range rawEntries {
		fields, err := sqsBatchObject(data, "Id", "ReceiptHandle")
		if err != nil {
			return err
		}
		if id, present := fields["Id"]; present {
			if !validSQSJSONString(id) || json.Unmarshal(id, &entries[i].Id) != nil {
				return sqsFailure{"InvalidParameterValue", "Entry Id must be a valid JSON string."}
			}
		}
		if receipt, present := fields["ReceiptHandle"]; present {
			if !validSQSJSONString(receipt) || json.Unmarshal(receipt, &entries[i].ReceiptHandle) != nil {
				return sqsFailure{"InvalidParameterValue", "ReceiptHandle must be a valid JSON string."}
			}
		}
	}
	return validateSQSDeleteBatchEntries(entries)
}

func validateSQSDeleteBatchEntries(entries []SQSDeleteMessageBatchEntry) error {
	if len(entries) == 0 {
		return sqsFailure{"EmptyBatchRequest", "The batch request must contain at least one entry."}
	}
	if len(entries) > 10 {
		return sqsFailure{"TooManyEntriesInBatchRequest", "The batch request can contain at most ten entries."}
	}
	ids := map[string]bool{}
	for _, entry := range entries {
		if !queueNamePattern.MatchString(entry.Id) {
			return sqsFailure{"InvalidBatchEntryId", "Entry Id must contain 1–80 ASCII letters, digits, hyphens or underscores."}
		}
		if ids[entry.Id] {
			return sqsFailure{"BatchEntryIdsNotDistinct", "Entry Id values must be unique within a batch."}
		}
		ids[entry.Id] = true
		if entry.ReceiptHandle == "" {
			return sqsFailure{"InvalidParameterValue", "ReceiptHandle is required for every entry."}
		}
	}
	return nil
}

// DeleteMessageBatch partitions semantic receipt errors while committing all
// valid deletions and scoped expiry cleanup together. Unlike AWS, stale receipts
// strictly fail locally. If distinct Ids reuse one handle, only the first can
// succeed; both result partitions retain request order. Neither rule promises
// AWS ordering or distributed-delivery behavior.
func (s *SQSStore) DeleteMessageBatch(ctx context.Context, region, queue string, entries []SQSDeleteMessageBatchEntry) (SQSDeleteMessageBatchResult, error) {
	if !validQueueName(queue) {
		return SQSDeleteMessageBatchResult{}, sqsFailure{"InvalidParameterValue", "A supported Standard queue name is required."}
	}
	if err := validateSQSDeleteBatchEntries(entries); err != nil {
		return SQSDeleteMessageBatchResult{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SQSDeleteMessageBatchResult{}, err
	}
	defer tx.Rollback()
	q, err := sqsQueueContextTx(ctx, tx, region, queue)
	if err != nil {
		return SQSDeleteMessageBatchResult{}, err
	}
	now := sqsNow()
	if err = purgeExpiredContextTx(ctx, tx, q, now); err != nil {
		return SQSDeleteMessageBatchResult{}, err
	}
	// An ignored purge must not turn an expired receipt into a valid target.
	checkExpiry := func() error {
		var remaining int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM aws_sqs_messages WHERE region=? AND queue=? AND sent<=?", region, queue, now-float64(q.RetentionSeconds)).Scan(&remaining); err != nil {
			return err
		}
		if remaining != 0 {
			return errors.New("SQS batch expiry cleanup left expired messages")
		}
		return nil
	}
	if err = checkExpiry(); err != nil {
		return SQSDeleteMessageBatchResult{}, err
	}
	result := SQSDeleteMessageBatchResult{Successful: []SQSDeleteMessageBatchSuccess{}, Failed: []SQSDeleteMessageBatchFailure{}}
	type target struct{ id, receipt string }
	var targets []target
	seen := map[string]bool{}
	// Resolve the complete batch before deleting live rows. Storage corruption
	// and later ignored writes cannot be relabeled as client receipt failures.
	for _, entry := range entries {
		var count int
		var id string
		if err = tx.QueryRowContext(ctx, "SELECT count(*),COALESCE(min(id),'') FROM aws_sqs_messages WHERE region=? AND queue=? AND receipt=?", region, queue, entry.ReceiptHandle).Scan(&count, &id); err != nil {
			return SQSDeleteMessageBatchResult{}, err
		}
		if count > 1 {
			return SQSDeleteMessageBatchResult{}, errors.New("SQS batch receipt identifies multiple persisted messages")
		}
		if count == 0 || seen[entry.ReceiptHandle] {
			result.Failed = append(result.Failed, SQSDeleteMessageBatchFailure{entry.Id, "ReceiptHandleIsInvalid", "The input receipt handle is invalid.", true})
		} else {
			targets = append(targets, target{id, entry.ReceiptHandle})
			result.Successful = append(result.Successful, SQSDeleteMessageBatchSuccess{entry.Id})
		}
		seen[entry.ReceiptHandle] = true
	}
	for _, target := range targets {
		written, err := tx.ExecContext(ctx, "DELETE FROM aws_sqs_messages WHERE region=? AND queue=? AND id=? AND receipt=?", region, queue, target.id, target.receipt)
		if err != nil {
			return SQSDeleteMessageBatchResult{}, err
		}
		if affected, err := written.RowsAffected(); err != nil {
			return SQSDeleteMessageBatchResult{}, err
		} else if affected != 1 {
			return SQSDeleteMessageBatchResult{}, errors.New("SQS batch deletion did not affect exactly one message")
		}
	}
	// A later trigger may resurrect an earlier target, including with a rotated
	// receipt. Acknowledge only when every selected message and receipt is absent.
	for _, target := range targets {
		var remaining int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM aws_sqs_messages WHERE region=? AND queue=? AND (id=? OR receipt=?)", region, queue, target.id, target.receipt).Scan(&remaining); err != nil {
			return SQSDeleteMessageBatchResult{}, err
		}
		if remaining != 0 {
			return SQSDeleteMessageBatchResult{}, errors.New("SQS batch deletion left an acknowledged message or receipt")
		}
	}
	current, err := sqsQueueContextTx(ctx, tx, region, queue)
	if err != nil || current != q {
		return SQSDeleteMessageBatchResult{}, errors.New("SQS batch queue changed during deletion")
	}
	if err = checkExpiry(); err != nil {
		return SQSDeleteMessageBatchResult{}, err
	}
	if err = ctx.Err(); err != nil {
		return SQSDeleteMessageBatchResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return SQSDeleteMessageBatchResult{}, err
	}
	return result, nil
}
