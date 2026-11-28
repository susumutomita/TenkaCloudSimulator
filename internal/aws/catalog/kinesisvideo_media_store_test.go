package catalog

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
	"github.com/susumutomita/TenkaCloudSimulator/internal/aws/kinesisvideo"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func videoStream(t *testing.T, h *harness, name string, hours int) row {
	t.Helper()
	out := h.rest("kinesisvideo", "POST", "/createStream", map[string]any{"StreamName": name, "DataRetentionInHours": hours})
	expect(t, out, 200, "")
	return videoRow(t, h.db, out.body["StreamARN"].(string))
}

func videoClip(t *testing.T) []byte {
	t.Helper()
	clip, err := os.ReadFile("../kinesisvideo/testdata/two-frames.mkv")
	if err != nil {
		t.Fatal(err)
	}
	return clip
}

func videoSaved(t *testing.T, h *harness, owner row, media []byte, start int64) kinesisReceipt {
	t.Helper()
	r, err := h.engine.saveVideoFragment(context.Background(), owner, start, media)
	if err != nil || r.number < 1 || r.cursor == "" {
		t.Fatalf("save %s: %+v, %v", owner.Name, r, err)
	}
	return r
}

func videoSQL(t *testing.T, db *sql.DB, statement string, args ...any) {
	t.Helper()
	if _, err := db.Exec(statement, args...); err != nil {
		t.Fatal(err)
	}
}

func videoFrame(t *testing.T, db *sql.DB) []string {
	t.Helper()
	var frame []string
	// Used with small source clips. Include the actual bytes and all persisted identities.
	for _, stmt := range []string{
		"SELECT json_array(service,region,kind,parent,id,name,arn,document,tags,seq,generation) FROM aws_catalog_resources ORDER BY seq",
		"SELECT json_array(number,stream_arn,owner_seq,cursor,producer_ms,server_ms,expires_ms,hex(media),hex(digest)) FROM aws_kinesisvideo_fragments ORDER BY number",
		"SELECT json_array(next) FROM aws_catalog_sequence",
		"SELECT json_array(last_ms) FROM aws_kinesisvideo_creation_clock",
		"SELECT json_array(name,seq) FROM sqlite_sequence WHERE name='aws_kinesisvideo_fragments'",
	} {
		rows, err := db.Query(stmt)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var text string
			if err := rows.Scan(&text); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			frame = append(frame, text)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		frame = append(frame, "end")
	}
	return frame
}

func videoSame(t *testing.T, label string, original, current []string) {
	t.Helper()
	if !reflect.DeepEqual(original, current) {
		t.Fatalf("%s changed its persisted frame (%d vs %d rows including table boundaries)", label, len(original), len(current))
	}
}

func videoReopen(t *testing.T, h *harness) {
	t.Helper()
	var path string
	if err := h.db.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&path); err != nil {
		t.Fatal(err)
	}
	cfg := h.engine.cfg
	if err := h.db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := core.New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	h.db = store.Database()
	h.engine, err = New(h.db, cfg, []*Service{kinesisVideo})
	if err != nil {
		t.Fatal(err)
	}
}

func videoHook(t *testing.T, db *sql.DB, hook func(sqlite3.AuthorizerActionCode, string, string, int64)) {
	t.Helper()
	c, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	err = c.Raw(func(raw any) error { raw.(driver.Conn).Raw().UpdateHook(hook); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestKinesisMediaStoreInterruptedWrite(t *testing.T) {
	for _, test := range []struct {
		op, table string
		action    sqlite3.AuthorizerActionCode
	}{{"save", "aws_kinesisvideo_fragments", sqlite3.AUTH_INSERT},
		{"create", "aws_catalog_resources", sqlite3.AUTH_INSERT},
		{"retention", "aws_catalog_resources", sqlite3.AUTH_UPDATE},
		{"delete", "aws_catalog_resources", sqlite3.AUTH_DELETE},
	} {
		op := test.op
		for _, cause := range []string{"cancel", "deadline"} {
			t.Run(op+"/"+cause, func(t *testing.T) {
				h, peer := newHarness(t, kinesisVideo), newHarness(t, kinesisVideo)
				media := videoClip(t)
				owner := videoStream(t, h, "camera", 24)
				videoSaved(t, h, owner, media, 100)
				videoSaved(t, peer, videoStream(t, peer, "camera", 24), media, 200)
				before, other := videoFrame(t, h.db), videoFrame(t, peer.db)
				ctx, cancel := context.WithCancel(context.Background())
				if cause == "deadline" {
					cancel()
					ctx, cancel = context.WithTimeout(context.Background(), 1500*time.Millisecond)
				}
				defer cancel()
				var witnessed atomic.Bool
				videoHook(t, h.db, func(action sqlite3.AuthorizerActionCode, _, table string, _ int64) {
					if action != test.action || table != test.table {
						return
					}
					if ctx.Err() != nil || !witnessed.CompareAndSwap(false, true) {
						return
					}
					if cause == "cancel" {
						cancel()
					} else {
						<-ctx.Done()
					}
				})
				run := func(ctx context.Context) bool {
					if op == "save" {
						r, err := h.engine.saveVideoFragment(ctx, owner, 300, media)
						return err == nil || r != (kinesisReceipt{})
					}
					path := "/createStream"
					fields := map[string]any{"StreamName": "interrupted", "DataRetentionInHours": 24}
					switch op {
					case "retention":
						path = "/updateDataRetention"
						fields = map[string]any{"StreamARN": owner.ARN, "CurrentVersion": owner.Document["Version"], "Operation": "DECREASE_DATA_RETENTION", "DataRetentionChangeInHours": 24}
					case "delete":
						path = "/deleteStream"
						fields = map[string]any{"StreamARN": owner.ARN, "CurrentVersion": owner.Document["Version"]}
					}
					data, err := json.Marshal(fields)
					if err != nil {
						t.Fatal(err)
					}
					req := httptest.NewRequest("POST", path, bytes.NewReader(data)).WithContext(ctx)
					sign(req, "kinesisvideo", "us-east-1")
					out := h.do(req)
					return out.status < 400 || out.body["StreamARN"] != nil
				}
				published := run(ctx)
				videoHook(t, h.db, nil)
				want := context.Canceled
				if cause == "deadline" {
					want = context.DeadlineExceeded
				}
				if !witnessed.Load() || !errors.Is(ctx.Err(), want) {
					t.Fatalf("post-write %s was not witnessed: %v", cause, ctx.Err())
				}
				videoReopen(t, h)
				after := videoFrame(t, h.db)
				if published || !reflect.DeepEqual(before, after) {
					t.Fatalf("post-write %s returned success/receipt=%v; original SQLite frame preserved=%v", cause, published, reflect.DeepEqual(before, after))
				}
				videoSame(t, "private peer", other, videoFrame(t, peer.db))
				if !run(context.Background()) {
					t.Fatal("fresh retry failed")
				}
				switch op {
				case "save":
					if len(videoBlocks(t, h, owner, "")) != 2 {
						t.Fatal("repaired write was not stored")
					}
				case "retention":
					if len(videoBlocks(t, h, owner, "")) != 0 {
						t.Fatal("repaired zero retention retained media")
					}
				case "delete":
					_, err := h.engine.readVideoFragments(context.Background(), owner, "")
					videoError(t, err, "ResourceNotFoundException")
				case "create":
					expect(t, h.rest("kinesisvideo", "POST", "/describeStream", map[string]any{"StreamName": "interrupted"}), 200, "")
				}
			})
		}
	}
}

func TestKinesisMediaStoreFragmentSaveFailure(t *testing.T) {
	for _, scenario := range []struct{ name, trigger string }{
		{"abort_digest", "BEFORE UPDATE OF digest ON aws_kinesisvideo_fragments BEGIN SELECT RAISE(ABORT,'interrupted digest'); END"},
		{"shifted_deadline", "AFTER UPDATE OF digest ON aws_kinesisvideo_fragments BEGIN UPDATE aws_kinesisvideo_fragments SET expires_ms=expires_ms+1 WHERE number=NEW.number; END"},
		{"changed_payload", "AFTER UPDATE OF digest ON aws_kinesisvideo_fragments BEGIN UPDATE aws_kinesisvideo_fragments SET media=zeroblob(length(NEW.media)) WHERE number=NEW.number; END"},
		{"removed_after_digest", "AFTER UPDATE OF digest ON aws_kinesisvideo_fragments BEGIN DELETE FROM aws_kinesisvideo_fragments WHERE number=NEW.number; END"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			h, peer := newHarness(t, kinesisVideo), newHarness(t, kinesisVideo)
			owner := videoStream(t, h, "camera", 24)
			clip := videoClip(t)
			videoSaved(t, h, owner, clip, 77)
			videoSaved(t, peer, videoStream(t, peer, "camera", 24), clip, 88)
			before, other := videoFrame(t, h.db), videoFrame(t, peer.db)
			videoSQL(t, h.db, "CREATE TEMP TRIGGER media_fault "+scenario.trigger)
			var staged atomic.Bool
			videoHook(t, h.db, func(action sqlite3.AuthorizerActionCode, _, table string, _ int64) {
				if action == sqlite3.AUTH_INSERT && table == "aws_kinesisvideo_fragments" {
					staged.Store(true)
				}
			})
			got, err := h.engine.saveVideoFragment(context.Background(), owner, 99, clip)
			videoHook(t, h.db, nil)
			videoReopen(t, h)
			after := videoFrame(t, h.db)
			if !staged.Load() || err == nil || got != (kinesisReceipt{}) || !reflect.DeepEqual(before, after) {
				t.Fatalf("actual insert=%t, returned success=%t, receipt=%t, preserved original state=%t", staged.Load(), err == nil, got != (kinesisReceipt{}), reflect.DeepEqual(before, after))
			}
			videoSaved(t, h, owner, clip, 99)
			videoSame(t, "private peer", other, videoFrame(t, peer.db))
		})
	}
}

func videoRow(t *testing.T, db *sql.DB, arn string) row {
	t.Helper()
	r, err := scanRow(db.QueryRow("SELECT "+columns+" FROM aws_catalog_resources WHERE arn=?", arn))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func videoError(t *testing.T, err error, code string) {
	t.Helper()
	var f *fault
	if !errors.As(err, &f) || f.code != code {
		t.Fatalf("wanted %s, got %v", code, err)
	}
}

func TestKinesisMediaStoreLegacyMigration(t *testing.T) {
	peer := newHarness(t, kinesisVideo)
	clip := videoClip(t)
	videoSaved(t, peer, videoStream(t, peer, "camera", 24), clip, 101)
	peerBefore := videoFrame(t, peer.db)
	path := filepath.Join(t.TempDir(), "metadata.db")
	old, err := core.New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = old.Close() })
	// The previous gateway had only the catalog tables. Use its insert function
	// and exact persisted stream shape, including stable versions and /1 ARNs.
	if _, err := newStore(old.Database()); err != nil {
		t.Fatal(err)
	}
	tx, err := old.Database().Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var arns []string
	for i, name := range []string{"camera", "another", "custom"} {
		arn := "arn:aws:kinesisvideo:us-east-1:000000000000:stream/" + name + "/1"
		key := "alias/aws/kinesisvideo"
		if name == "custom" {
			key = "arn:aws:kms:us-east-1:000000000000:key/01234567-89ab-4cde-8fab-0123456789ab"
		}
		record := row{Service: "kinesisvideo", Region: "us-east-1", Kind: "stream", ID: name, Name: name, ARN: arn,
			Document: map[string]any{"StreamARN": arn, "StreamName": name, "DeviceName": "source-" + name, "MediaType": "video/h264", "Status": "CREATING", "CreationTime": "2026-11-01T12:00:00Z", "Version": fmt.Sprintf("%020d", i+1), "KmsKeyId": key, "DataRetentionInHours": 24},
			Tags:     map[string]string{"device": name, "fixture": "legacy"}}
		if err := insertNewResource(tx, kinesisVideo.resource("stream"), &record, nil); err != nil {
			t.Fatal(err)
		}
		arns = append(arns, arn)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var originals []row
	for _, arn := range arns {
		originals = append(originals, videoRow(t, old.Database(), arn))
	}
	videoSQL(t, old.Database(), `CREATE TRIGGER fail_second_upgrade BEFORE UPDATE OF document ON aws_catalog_resources
 WHEN json_extract(NEW.document,'$.Status')='ACTIVE' AND
 (SELECT count(*) FROM aws_catalog_resources WHERE json_extract(document,'$.Status')='ACTIVE')=1
 BEGIN SELECT RAISE(ABORT,'later upgrade failed'); END`)
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := core.New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	db := store.Database()
	var upgraded atomic.Int64
	videoHook(t, db, func(action sqlite3.AuthorizerActionCode, _, table string, _ int64) {
		if action == sqlite3.AUTH_UPDATE && table == "aws_catalog_resources" {
			upgraded.Add(1)
		}
	})
	if _, err := New(db, peer.engine.cfg, []*Service{kinesisVideo}); err == nil {
		t.Fatal("failed second upgrade returned a gateway")
	}
	videoHook(t, db, nil)
	var mediaTables int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_schema WHERE type='table' AND name LIKE 'aws_kinesisvideo_%'").Scan(&mediaTables); err != nil {
		t.Fatal(err)
	}
	if upgraded.Load() != 1 || mediaTables != 0 {
		t.Fatalf("observed %d upgrades before failure, %d new media tables remain", upgraded.Load(), mediaTables)
	}
	for _, old := range originals {
		if !reflect.DeepEqual(old, videoRow(t, db, old.ARN)) {
			t.Fatalf("partial migration of %s", old.Name)
		}
	}
	videoSQL(t, db, "DROP TRIGGER fail_second_upgrade")
	engine, err := New(db, peer.engine.cfg, []*Service{kinesisVideo})
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, engine: engine, db: db}
	for _, want := range originals {
		if want.Name != "custom" {
			want.Document["Status"] = "ACTIVE"
			want.Generation++
		}
		actual := videoRow(t, db, want.ARN)
		if !reflect.DeepEqual(want, actual) {
			t.Fatalf("migrated record changed unexpected fields: want %+v, got %+v", want, actual)
		}
		if want.Name == "custom" {
			receipt, err := engine.saveVideoFragment(context.Background(), actual, 100, clip)
			videoError(t, err, "InvalidArgumentException")
			if receipt != (kinesisReceipt{}) {
				t.Fatal("metadata-only stream returned a media receipt")
			}
		} else {
			videoSaved(t, h, actual, clip, 102)
		}
	}
	after := videoFrame(t, db)
	videoReopen(t, h)
	videoSame(t, "second startup", after, videoFrame(t, h.db))
	videoSame(t, "private peer", peerBefore, videoFrame(t, peer.db))
}

func videoBlocks(t *testing.T, h *harness, r row, cursor string) [][]byte {
	t.Helper()
	b, err := h.engine.readVideoFragments(context.Background(), r, cursor)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func videoRetention(t *testing.T, h *harness, r row, operation string, change int) {
	t.Helper()
	current := videoRow(t, h.db, r.ARN)
	expect(t, h.rest("kinesisvideo", "POST", "/updateDataRetention", map[string]any{"StreamARN": r.ARN, "CurrentVersion": current.Document["Version"], "Operation": operation + "_DATA_RETENTION", "DataRetentionChangeInHours": change}), 200, "")
}

func TestKinesisMediaStoreRetention(t *testing.T) {
	for _, test := range []string{"expiry", "extend_live", "extend_expired", "shorten", "zero"} {
		t.Run(test, func(t *testing.T) {
			h, peer := newHarness(t, kinesisVideo), newHarness(t, kinesisVideo)
			clip := videoClip(t)
			videoSaved(t, peer, videoStream(t, peer, "camera", 24), clip, 1)
			other := videoFrame(t, peer.db)
			owner := videoStream(t, h, "camera", 2)
			base := h.now
			first := videoSaved(t, h, owner, clip, 9)
			h.now = base.Add(time.Hour)
			second := videoSaved(t, h, owner, clip, 15)
			switch test {
			case "expiry":
				h.now = base.Add(2*time.Hour - time.Millisecond)
				if len(videoBlocks(t, h, owner, "")) != 2 {
					t.Fatal("fragment disappeared before expiry")
				}
				h.now = base.Add(2 * time.Hour)
			case "extend_live":
				h.now = base.Add(2*time.Hour - time.Millisecond)
				videoRetention(t, h, owner, "INCREASE", 1)
				h.now = base.Add(3*time.Hour - time.Millisecond)
				if len(videoBlocks(t, h, owner, "")) != 2 {
					t.Fatal("extension lost a live fragment")
				}
				h.now = base.Add(3 * time.Hour)
			case "extend_expired":
				// No read between the old deadline and extension: the update
				// itself must remove already expired fragments before extending.
				h.now = base.Add(2 * time.Hour)
				videoRetention(t, h, owner, "INCREASE", 1)
				h.now = base.Add(4*time.Hour - time.Millisecond)
			case "shorten":
				h.now = base.Add(time.Hour + time.Millisecond)
				videoRetention(t, h, owner, "DECREASE", 1)
			case "zero":
				videoRetention(t, h, owner, "DECREASE", 2)
				r, err := h.engine.saveVideoFragment(context.Background(), owner, 76, clip)
				if err != nil || r.number != 0 || r.cursor != "" {
					t.Fatalf("zero retention returned %+v, %v", r, err)
				}
			}
			blocks := videoBlocks(t, h, owner, "")
			if test == "zero" {
				if len(blocks) != 0 {
					t.Fatal("zero retention kept old fragments")
				}
			} else if len(blocks) != 1 || !bytes.Contains(blocks[0], []byte(second.cursor)) {
				t.Fatal("unexpected surviving media/receipt")
			}
			_, err := h.engine.readVideoFragments(context.Background(), owner, first.cursor)
			videoError(t, err, "InvalidArgumentException")
			h.now = base
			videoReopen(t, h)
			if !reflect.DeepEqual(blocks, videoBlocks(t, h, owner, "")) {
				t.Fatal("backward clock or reopen resurrected discarded bytes")
			}
			videoSame(t, "private peer", other, videoFrame(t, peer.db))
		})
	}
}

func TestKinesisMediaStoreCreationClock(t *testing.T) {
	h, peer := newHarness(t, kinesisVideo), newHarness(t, kinesisVideo)
	clip := videoClip(t)
	before := videoStream(t, h, "camera", 24)
	old := videoSaved(t, h, before, clip, 30)
	videoSaved(t, peer, videoStream(t, peer, "camera", 24), clip, 31)
	other := videoFrame(t, peer.db)
	h.now = h.now.Add(-time.Hour)
	// Synchronize both policy decisions against the same observed creation
	// clock. One writer must retry and authorize its new physical ARN.
	original := h.engine.cfg.Authorize
	arrivals := make(chan string, 2)
	release := make(chan struct{})
	var attempts atomic.Int64
	h.engine.cfg.Authorize = func(ctx context.Context, principal, action, arn string) (string, error) {
		if action == "kinesisvideo:CreateStream" && attempts.Add(1) <= 2 {
			arrivals <- arn
			select {
			case <-release:
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		return original(ctx, principal, action, arn)
	}
	results := make(chan reply, 2)
	for _, name := range []string{"left", "right"} {
		go func() { results <- h.rest("kinesisvideo", "POST", "/createStream", map[string]any{"StreamName": name}) }()
	}
	for range 2 {
		select {
		case <-arrivals:
		case <-time.After(5 * time.Second):
			close(release)
			t.Fatal("creators did not reach concurrent authorization")
		}
	}
	close(release)
	suffix := func(arn string) int64 {
		pieces := strings.Split(arn, "/")
		n, err := strconv.ParseInt(pieces[len(pieces)-1], 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	first := suffix(before.ARN)
	seen := map[int64]bool{}
	for range 2 {
		r := <-results
		expect(t, r, 200, "")
		arn := r.body["StreamARN"].(string)
		if videoRow(t, h.db, arn).ARN != arn {
			t.Fatal("returned ARN differs from saved ARN")
		}
		seen[suffix(arn)] = true
	}
	if attempts.Load() < 3 || len(seen) != 2 || !seen[first+1] || !seen[first+2] {
		t.Fatalf("creation attempts=%d, committed timestamps=%v, original=%d", attempts.Load(), seen, first)
	}
	expect(t, h.rest("kinesisvideo", "POST", "/deleteStream", map[string]any{"StreamARN": before.ARN, "CurrentVersion": before.Document["Version"]}), 200, "")
	videoReopen(t, h)
	replacement := videoStream(t, h, "camera", 24)
	if replacement.Seq == before.Seq || suffix(replacement.ARN) != first+3 {
		t.Fatalf("replacement reused identity: old %s seq %d, new %s seq %d", before.ARN, before.Seq, replacement.ARN, replacement.Seq)
	}
	_, err := h.engine.readVideoFragments(context.Background(), before, old.cursor)
	videoError(t, err, "ResourceNotFoundException")
	got, err := h.engine.saveVideoFragment(context.Background(), before, 40, clip)
	videoError(t, err, "ResourceNotFoundException")
	if got != (kinesisReceipt{}) {
		t.Fatal("retired physical generation returned receipt")
	}
	_, err = h.engine.readVideoFragments(context.Background(), replacement, old.cursor)
	videoError(t, err, "InvalidArgumentException")
	if len(videoBlocks(t, h, replacement, "")) != 0 {
		t.Fatal("replacement inherited old media")
	}
	videoSame(t, "private peer", other, videoFrame(t, peer.db))
}

func videoRefusedByLimit(t *testing.T, h *harness, r row, media []byte) {
	t.Helper()
	receipt, err := h.engine.saveVideoFragment(context.Background(), r, 303, media)
	videoError(t, err, "ClientLimitExceededException")
	if receipt != (kinesisReceipt{}) {
		t.Fatal("rejected media returned a receipt")
	}
}

func TestKinesisMediaStoreCountQuota(t *testing.T) {
	h, peer := newHarness(t, kinesisVideo), newHarness(t, kinesisVideo)
	clip := videoClip(t)
	peerOwner := videoStream(t, peer, "camera", 24)
	videoSaved(t, peer, peerOwner, clip, 22)
	other := videoFrame(t, peer.db)
	owner := videoStream(t, h, "camera", 24)
	for i := range 15 {
		videoSaved(t, h, owner, clip, int64(i))
	}
	type writeResult struct {
		receipt kinesisReceipt
		err     error
	}
	out := make(chan writeResult, 2)
	gate := make(chan struct{})
	for _, start := range []int64{250, 251} {
		go func() {
			<-gate
			r, err := h.engine.saveVideoFragment(context.Background(), owner, start, clip)
			out <- writeResult{r, err}
		}()
	}
	close(gate)
	var success int
	for range 2 {
		result := <-out
		if result.err != nil {
			videoError(t, result.err, "ClientLimitExceededException")
			if result.receipt != (kinesisReceipt{}) {
				t.Fatal("losing writer got a receipt")
			}
			continue
		}
		success++
		blocks := videoBlocks(t, h, owner, "")
		if len(blocks) != 16 || !bytes.Contains(blocks[15], []byte(result.receipt.cursor)) {
			t.Fatal("accepted 16th fragment is missing")
		}
	}
	if success != 1 {
		t.Fatalf("%d writers accepted in last slot", success)
	}
	fullStream := videoFrame(t, h.db)
	videoRefusedByLimit(t, h, owner, clip)
	videoSame(t, "stream limit", fullStream, videoFrame(t, h.db))
	for i := range 7 {
		r := videoStream(t, h, fmt.Sprintf("extra-%d", i), 24)
		for j := range 16 {
			videoSaved(t, h, r, clip, int64(j))
		}
	}
	extra := videoStream(t, h, "otherwise-empty", 24)
	full := videoFrame(t, h.db)
	videoRefusedByLimit(t, h, extra, clip)
	videoReopen(t, h)
	videoSame(t, "team limit", full, videoFrame(t, h.db))
	videoSame(t, "private peer", other, videoFrame(t, peer.db))
	videoSaved(t, peer, peerOwner, clip, 44)
}

// A Matroska Void pads the finite segment without changing either AVC frame.
// Both byte limits are reached by real accepted fragments through the store.
func videoPaddedClip(t *testing.T, clip []byte, size int) []byte {
	t.Helper()
	offset := bytes.Index(clip, []byte{0x18, 0x53, 0x80, 0x67}) + 4
	if offset < 4 || offset+8 > len(clip) || clip[offset] != 1 || binary.BigEndian.Uint64(clip[offset:offset+8]) != (1<<56|uint64(len(clip)-offset-8)) || size < len(clip)+5 {
		t.Fatal("unexpected test segment")
	}
	b := make([]byte, size)
	copy(b, clip)
	binary.BigEndian.PutUint64(b[offset:offset+8], 1<<56|uint64(size-offset-8))
	b[len(clip)] = 0xec
	binary.BigEndian.PutUint32(b[len(clip)+1:], 1<<28|uint32(size-len(clip)-5))
	if parsed, err := kinesisvideo.ParseFragment(b); err != nil || parsed.Frames != 2 {
		t.Fatalf("invalid padded segment of %d bytes: %v", size, err)
	}
	return b
}

func TestKinesisMediaStoreByteQuota(t *testing.T) {
	h, peer := newHarness(t, kinesisVideo), newHarness(t, kinesisVideo)
	clip := videoClip(t)
	peerOwner := videoStream(t, peer, "left", 24)
	videoSaved(t, peer, peerOwner, clip, 66)
	other := videoFrame(t, peer.db)
	left := videoStream(t, h, "left", 24)
	right := videoStream(t, h, "right", 24)
	empty := videoStream(t, h, "last-slot", 24)
	large := videoPaddedClip(t, clip, 4<<20)
	nearly := videoPaddedClip(t, clip, 4<<20-4096)
	exact := videoPaddedClip(t, clip, 4096)
	oneMore := videoPaddedClip(t, clip, 4097)
	type quotaSnapshot struct{ count, bytes, last int64 }
	totals := func() quotaSnapshot {
		var s quotaSnapshot
		if err := h.db.QueryRow("SELECT count(*),coalesce(sum(length(media)),0),coalesce(max(number),0) FROM aws_kinesisvideo_fragments").Scan(&s.count, &s.bytes, &s.last); err != nil {
			t.Fatal(err)
		}
		return s
	}
	for _, r := range []row{left, right} {
		for i := range 7 {
			videoSaved(t, h, r, large, int64(i))
		}
		videoSaved(t, h, r, nearly, 71)
		target := r
		if r.ARN == right.ARN {
			target = empty
		} // no stream budget constraint on the team-wide check
		before := totals()
		videoRefusedByLimit(t, h, target, oneMore)
		if totals() != before {
			t.Fatal("one byte above the budget changed stored rows/bytes/sequence")
		}
		videoSaved(t, h, target, exact, 72)
	}
	videoReopen(t, h)
	got := totals()
	if got.count != 18 || got.bytes != (64<<20) {
		t.Fatalf("full database has %d fragments, %d bytes", got.count, got.bytes)
	}
	videoRefusedByLimit(t, h, empty, clip)
	if totals() != got {
		t.Fatal("full team accepted additional bytes")
	}
	videoSame(t, "private peer", other, videoFrame(t, peer.db))
	videoSaved(t, peer, peerOwner, clip, 88)
}
