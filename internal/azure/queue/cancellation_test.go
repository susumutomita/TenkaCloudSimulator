package queue

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// Observe the request's cancellation channel at transaction admission, after
// authorization and request validation. The caller controls cancellation.
type observedDone struct {
	context.Context
	ready chan error
	once  sync.Once
}

func (c *observedDone) Done() <-chan struct{} {
	done := c.Context.Done()
	c.once.Do(func() { c.ready <- c.Context.Err() })
	return done
}

type observedBodyEOF struct {
	io.Reader
	observe func()
}

func (r observedBodyEOF) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if err == io.EOF {
		r.observe()
	}
	return n, err
}

func TestAzureQueueCancellationWhileWaitingForTransaction(t *testing.T) {
	h, _, now := fixture(t)
	request(t, h, http.MethodPut, qp, "", http.StatusCreated)
	const (
		path      = qp + "/messages"
		first     = "<QueueMessage><MessageText>first</MessageText></QueueMessage>"
		cancelled = "<QueueMessage><MessageText>cancelled</MessageText></QueueMessage>"
		watchdog  = 3 * time.Second
	)
	entered, release := make(chan struct{}), make(chan struct{})
	h.cfg.Now = sync.OnceValue(func() time.Time {
		// The first request already owns the Queue gate and an actual SQLite
		// transaction when it reaches the clock. Keep both until verified.
		close(entered)
		<-release
		return *now
	})
	var workers sync.WaitGroup
	var once sync.Once
	releaseFirst := func() { once.Do(func() { close(release) }) }
	ctx, cancel := context.WithCancel(context.Background())
	defer func() {
		cancel()
		releaseFirst()
		workers.Wait()
	}()
	send := func(ctx context.Context, body io.Reader) <-chan *httptest.ResponseRecorder {
		r := httptest.NewRequestWithContext(ctx, http.MethodPost, path, body)
		r.Header.Set("x-ms-version", apiVersion)
		w := httptest.NewRecorder()
		result := make(chan *httptest.ResponseRecorder, 1)
		workers.Go(func() {
			h.ServeHTTP(w, r)
			result <- w
		})
		return result
	}
	firstResult := send(context.Background(), strings.NewReader(first))
	select {
	case <-entered:
	case <-time.After(watchdog):
		t.Fatal("first real transaction never entered")
	}
	waiter := &observedDone{Context: ctx, ready: make(chan error, 1)}
	bodyRead := make(chan error, 1)
	var bodyOnce sync.Once
	waiterResult := send(waiter, observedBodyEOF{strings.NewReader(cancelled), func() {
		bodyOnce.Do(func() { bodyRead <- ctx.Err() })
	}})
	select {
	case err := <-bodyRead:
		if err != nil {
			t.Fatalf("waiter was not live at the end of valid XML input: %v", err)
		}
	case <-time.After(watchdog):
		t.Fatal("queued request never read its valid body")
	}
	observed := false
	select {
	case err := <-waiter.ready:
		if err != nil {
			t.Fatalf("waiter was not live at transaction admission: %v", err)
		}
		observed = true
	case got := <-waiterResult:
		t.Fatalf("queued request returned before cancellation: %d", got.Code)
	case <-time.After(watchdog):
		// The previous mutex never checks the context here. Still cancel
		// after its body read and before releasing the holder, then require
		// the same observable result.
	}
	cancel()
	select {
	case got := <-waiterResult:
		if !observed {
			t.Fatal("queued request never observed a live context at transaction admission")
		}
		if got.Code != http.StatusInternalServerError || !strings.Contains(got.Body.String(), "<Code>InternalError</Code>") {
			t.Fatalf("cancelled request: %d: %s", got.Code, got.Body.String())
		}
	case <-time.After(watchdog):
		t.Fatal("cancelled queued request did not exit while first transaction remained held")
	}
	releaseFirst()
	select {
	case got := <-firstResult:
		if got.Code != http.StatusCreated {
			t.Fatalf("first request: %d: %s", got.Code, got.Body.String())
		}
	case <-time.After(watchdog):
		t.Fatal("first request did not finish after release")
	}
	peek := messages(t, request(t, h, http.MethodGet, path+"?peekonly=true&numofmessages=32", "", http.StatusOK))
	if len(peek) != 1 || peek[0].Text == nil || *peek[0].Text != "first" || peek[0].Count != 0 {
		t.Fatal("cancellation changed visible messages or the independent transaction")
	}
	// The same ordinary request with a live context can use the gate again.
	request(t, h, http.MethodPost, path, cancelled, http.StatusCreated)
	request(t, h, http.MethodDelete, qp, "", http.StatusNoContent)
}
