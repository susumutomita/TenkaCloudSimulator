package aws

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestEC2GuestAttachmentNilAndForeignHandlesAreRefused(t *testing.T) {
	var nilReceipt *EC2GuestAttachmentReceipt
	if nilReceipt.Scope() != nil || (&EC2GuestAttachmentReceipt{}).Scope() != nil {
		t.Fatal("nil receipt exposed a scope")
	}
	if consumed, transferred, constructing := nilReceipt.Ownership(); consumed || transferred || constructing {
		t.Fatal("nil receipt reported ownership")
	}
	if _, err := NewEC2GuestAttachmentCoordinator(nil, strings.Repeat("a", 64)); err == nil {
		t.Fatal("coordinator without store")
	}
	c, lease, r, p, ctx := attachmentFixture(t)
	for _, supervisor := range []string{"", "short", strings.Repeat("A", 64)} {
		if _, err := NewEC2GuestAttachmentCoordinator(c.store, supervisor); err == nil {
			t.Fatalf("supervisor %q accepted", supervisor)
		}
	}
	var nilCtx context.Context
	var nilCoordinator *EC2GuestAttachmentCoordinator
	if _, err := nilCoordinator.Advance(ctx, lease); err == nil {
		t.Fatal("nil coordinator advanced")
	}
	if _, err := nilCoordinator.Get(ctx, lease.Owner); err == nil {
		t.Fatal("nil coordinator read")
	}
	if _, err := c.Get(nilCtx, lease.Owner); err == nil {
		t.Fatal("nil context read")
	}
	bad := lease.Owner
	bad.Account = "x"
	if _, err := c.Get(ctx, bad); err == nil {
		t.Fatal("invalid owner read")
	}
	if _, err := c.Get(ctx, lease.Owner); err == nil {
		t.Fatal("status invented before any attachment")
	}

	receipt, err := c.Attach(ctx, ctx, lease, r, p, time.Now().Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = c.Seal(context.Background(), receipt); _ = c.ObserveCleanup(ctx, receipt) }()
	if _, err = c.Attach(ctx, ctx, lease, r, p, time.Now().Add(time.Second)); !errors.Is(err, ErrEC2GuestAttachmentPending) {
		t.Fatal("second live attachment for one owner", err)
	}
	if err = c.ObserveCleanup(ctx, receipt); !errors.Is(err, ErrEC2GuestAttachmentPending) {
		t.Fatal("cleanup observed before seal", err)
	}
	other, err := NewEC2GuestAttachmentCoordinator(c.store, strings.Repeat("c", 64))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Seal(nilCtx, receipt); err == nil {
		t.Fatal("sealed with nil context")
	}
	for name, h := range map[string]*EC2GuestAttachmentReceipt{"nil": nil, "empty": {}, "forged": {entry: &ec2AttachmentEntry{}}} {
		if _, err = c.Seal(ctx, h); err == nil {
			t.Fatalf("%s receipt sealed", name)
		}
		if err = c.ObserveCleanup(ctx, h); err == nil {
			t.Fatalf("%s receipt cleaned", name)
		}
	}
	if _, err = other.Seal(ctx, receipt); err == nil {
		t.Fatal("foreign coordinator sealed the receipt")
	}
	if err = other.ObserveCleanup(ctx, receipt); err == nil {
		t.Fatal("foreign coordinator observed cleanup")
	}
	status, err := c.Get(ctx, lease.Owner)
	if err != nil || status.State != "attached" || status.RecoveryRequired || status.Issuance.Lease != lease {
		t.Fatal(status, err)
	}
}

func TestEC2GuestAttachmentAttachRefusesInvalidInputsUnconsumed(t *testing.T) {
	c, lease, r, p, ctx := attachmentFixture(t)
	r.onStream = func() { t.Error("refused attachment consumed resource") }
	deadline := time.Now().Add(time.Second)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	var nilCtx context.Context
	var typedNil *attachmentMemoryResource
	released := lease
	released.State = "released"
	invalid := lease
	invalid.Owner.Account = "x"
	cases := map[string]func() (*EC2GuestAttachmentReceipt, error){
		"nil coordinator": func() (*EC2GuestAttachmentReceipt, error) {
			return (*EC2GuestAttachmentCoordinator)(nil).Attach(ctx, ctx, lease, r, p, deadline)
		},
		"nil admission":  func() (*EC2GuestAttachmentReceipt, error) { return c.Attach(nilCtx, ctx, lease, r, p, deadline) },
		"nil lifetime":   func() (*EC2GuestAttachmentReceipt, error) { return c.Attach(ctx, nilCtx, lease, r, p, deadline) },
		"nil resource":   func() (*EC2GuestAttachmentReceipt, error) { return c.Attach(ctx, ctx, lease, nil, p, deadline) },
		"typed nil":      func() (*EC2GuestAttachmentReceipt, error) { return c.Attach(ctx, ctx, lease, typedNil, p, deadline) },
		"invalid lease":  func() (*EC2GuestAttachmentReceipt, error) { return c.Attach(ctx, ctx, invalid, r, p, deadline) },
		"released lease": func() (*EC2GuestAttachmentReceipt, error) { return c.Attach(ctx, ctx, released, r, p, deadline) },
		"canceled":       func() (*EC2GuestAttachmentReceipt, error) { return c.Attach(canceled, ctx, lease, r, p, deadline) },
		"unbounded lifetime": func() (*EC2GuestAttachmentReceipt, error) {
			return c.Attach(ctx, context.Background(), lease, r, p, deadline)
		},
		"zero deadline": func() (*EC2GuestAttachmentReceipt, error) { return c.Attach(ctx, ctx, lease, r, p, time.Time{}) },
		"deadline past lifetime": func() (*EC2GuestAttachmentReceipt, error) {
			return c.Attach(ctx, ctx, lease, r, p, time.Now().Add(time.Hour))
		},
	}
	for name, f := range cases {
		if receipt, err := f(); err == nil || receipt != nil {
			t.Errorf("%s accepted: %v", name, err)
		}
	}
	for name, edit := range map[string]func(*attachmentMemoryResource){
		"zero incarnation":     func(m *attachmentMemoryResource) { m.issuance.BackendIncarnation = [32]byte{} },
		"incarnation mismatch": func(m *attachmentMemoryResource) { m.issuance.BackendIncarnation = [32]byte{9} },
		"policy generation":    func(m *attachmentMemoryResource) { m.issuance.PolicyGeneration = "nope" },
	} {
		m := *r
		edit(&m)
		if receipt, err := c.Attach(ctx, ctx, lease, &m, p, deadline); err == nil || receipt != nil {
			t.Errorf("%s accepted: %v", name, err)
		}
	}
	var count int
	if err := c.store.db.QueryRow(`SELECT count(*) FROM aws_ec2_guest_attachments`).Scan(&count); err != nil || count != 0 {
		t.Fatal("refused attachment journaled", count, err)
	}
	if p.closes.Load() != 0 {
		t.Fatal("caller service consumed")
	}
}

// Durable attachment rows are recovery metadata; any non-canonical or
// inconsistent row is refused rather than trusted.
func TestEC2GuestAttachmentRecordDecodingRejectsTampering(t *testing.T) {
	c, lease, r, p, ctx := attachmentFixture(t)
	receipt, err := c.Attach(ctx, ctx, lease, r, p, time.Now().Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = c.Seal(context.Background(), receipt); _ = c.ObserveCleanup(ctx, receipt) }()
	var raw string
	if err = c.store.db.QueryRow(`SELECT record FROM aws_ec2_guest_attachments`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	good, err := decodeEC2AttachmentRecord(raw)
	if err != nil {
		t.Fatal(err)
	}
	for name, bad := range map[string]string{"empty": "", "oversized": strings.Repeat(" ", 32769), "not JSON": "{", "non-canonical": " " + raw} {
		if _, err = decodeEC2AttachmentRecord(bad); err == nil {
			t.Errorf("%s record decoded", name)
		}
	}
	for name, edit := range map[string]func(*ec2AttachmentRecord){
		"lease state":       func(x *ec2AttachmentRecord) { x.Issuance.Lease.State = "released" },
		"invalid lease":     func(x *ec2AttachmentRecord) { x.Issuance.Lease.Owner.Account = "x" },
		"supervisor":        func(x *ec2AttachmentRecord) { x.Supervisor = "nope" },
		"policy generation": func(x *ec2AttachmentRecord) { x.Issuance.PolicyGeneration = "nope" },
		"zero incarnation":  func(x *ec2AttachmentRecord) { x.Issuance.BackendIncarnation = [32]byte{} },
		"binding mismatch":  func(x *ec2AttachmentRecord) { x.Descriptor.Binding.BackendIncarnation = [32]byte{7} },
		"zero deadline":     func(x *ec2AttachmentRecord) { x.Deadline = time.Time{} },
		"zero lifetime":     func(x *ec2AttachmentRecord) { x.OriginalLifetime = time.Time{} },
		"deadline overrun":  func(x *ec2AttachmentRecord) { x.Deadline = x.OriginalLifetime.Add(time.Second) },
	} {
		x := good
		var clone ec2AttachmentRecord
		if b, e := json.Marshal(x); e != nil || json.Unmarshal(b, &clone) != nil {
			t.Fatal(e)
		}
		edit(&clone)
		b, e := json.Marshal(clone)
		if e != nil {
			t.Fatal(e)
		}
		if _, err = decodeEC2AttachmentRecord(string(b)); err == nil {
			t.Errorf("%s record decoded", name)
		}
	}
	// A durable row whose record names another owner is refused on read.
	foreign := good
	foreign.Issuance.Lease.Owner.Instance = "i-other"
	b, err := json.Marshal(foreign)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.store.db.Exec(`UPDATE aws_ec2_guest_attachments SET record=?`, string(b)); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Get(ctx, lease.Owner); err == nil {
		t.Fatal("foreign record read as status")
	}
	if _, err = c.store.db.Exec(`UPDATE aws_ec2_guest_attachments SET record=?`, raw); err != nil {
		t.Fatal(err)
	}
	if status, e := c.Get(ctx, lease.Owner); e != nil || status.Supervisor != strings.Repeat("a", 64) {
		t.Fatal("restored record unreadable", e)
	}
}
