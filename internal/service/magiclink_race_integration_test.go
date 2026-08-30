//go:build integration

package service_test

import (
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"recruiting/internal/service"
)

func TestApplyLinkConsumeIsSingleUseUnderConcurrency(t *testing.T) {
	f := newFixture(t)
	for round := 0; round < 5; round++ {
		tok, _, err := f.links.Issue(f.ctx, f.admin(), service.LinkApply, uuid.New(), time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		const n = 16
		var wg sync.WaitGroup
		start := make(chan struct{})
		var mu sync.Mutex
		ok := 0
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				if _, err := f.links.Consume(f.ctx, tok, service.LinkApply); err == nil {
					mu.Lock()
					ok++
					mu.Unlock()
				}
			}()
		}
		close(start)
		wg.Wait()
		t.Logf("round %d: %d of %d concurrent Consume calls succeeded on one apply link", round, ok, n)
		if ok != 1 {
			t.Errorf("expected exactly one success, got %d", ok)
		}
	}
}
