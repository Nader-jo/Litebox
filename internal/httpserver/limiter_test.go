package httpserver

import (
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWindowLimiter(t *testing.T) {
	now := time.Date(2026, time.August, 10, 12, 0, 0, 0, time.UTC)
	limiter := newWindowLimiter(2, time.Hour)
	limiter.now = func() time.Time { return now }
	if !limiter.take("user") {
		t.Fatal("limiter rejected the first event")
	}
	if !limiter.take("user") {
		t.Fatal("limiter rejected an event within the configured limit")
	}
	if limiter.take("user") {
		t.Fatal("limiter accepted an event beyond the configured limit")
	}
	if !limiter.take("another-user") {
		t.Fatal("one user's limit affected another user")
	}
	now = now.Add(time.Hour + time.Second)
	if !limiter.take("user") {
		t.Fatal("expired events were not released")
	}
}

func TestWindowLimiterHashesUntrustedKeys(t *testing.T) {
	limiter := newWindowLimiter(2, time.Hour)
	if !limiter.take(strings.Repeat("x", 1<<20)) {
		t.Fatal("limiter rejected the first event")
	}
	for key := range limiter.events {
		if len(key) != 32 {
			t.Fatalf("stored limiter key length = %d, want 32", len(key))
		}
	}
}

func TestLoginLimiterChecksIPBeforeCreatingAccountBucket(t *testing.T) {
	server := &Server{loginIP: newWindowLimiter(1, time.Hour), loginAccount: newWindowLimiter(5, time.Hour)}
	if !server.takeLoginAttempt("192.0.2.1", "first@example.com") {
		t.Fatal("first attempt was rejected")
	}
	if server.takeLoginAttempt("192.0.2.1", "second@example.com") {
		t.Fatal("exhausted IP bucket accepted another attempt")
	}
	if len(server.loginAccount.events) != 1 {
		t.Fatalf("account buckets = %d, want 1", len(server.loginAccount.events))
	}
}

func TestClientIPWalksForwardedChainFromRightToLeft(t *testing.T) {
	_, directProxy, _ := net.ParseCIDR("10.0.0.0/8")
	_, internalProxy, _ := net.ParseCIDR("192.168.0.0/16")
	server := &Server{trusted: []*net.IPNet{directProxy, internalProxy}}
	request := httptest.NewRequest("GET", "http://example.test", nil)
	request.RemoteAddr = "10.0.0.2:1234"
	request.Header.Set("X-Forwarded-For", "198.51.100.99, 203.0.113.7")
	if actual := server.clientIP(request); actual != "203.0.113.7" {
		t.Fatalf("spoofed forwarded chain returned %q", actual)
	}
	request.Header.Set("X-Forwarded-For", "198.51.100.99, 192.168.1.5")
	if actual := server.clientIP(request); actual != "198.51.100.99" {
		t.Fatalf("trusted proxy chain returned %q", actual)
	}
}

func TestResetLimiterUsesSeparateIPAndAccountBuckets(t *testing.T) {
	server := &Server{resetIP: newWindowLimiter(1, time.Hour), resetAccount: newWindowLimiter(1, time.Hour)}
	if !server.takeResetAttempt("192.0.2.1", "owner@example.com") {
		t.Fatal("first reset attempt was rejected")
	}
	if server.takeResetAttempt("192.0.2.2", "owner@example.com") {
		t.Fatal("account bucket allowed a distributed reset attempt")
	}
	if server.takeResetAttempt("192.0.2.1", "other@example.com") {
		t.Fatal("IP bucket allowed another reset attempt")
	}
}

func TestWindowLimiterAtomicallyCapsConcurrentEventsAndBoundsKeys(t *testing.T) {
	limiter := newWindowLimiter(3, time.Hour)
	limiter.maxKeys = 2
	start := make(chan struct{})
	results := make(chan bool, 20)
	for range 20 {
		go func() {
			<-start
			results <- limiter.take("same-key")
		}()
	}
	close(start)
	accepted := 0
	for range 20 {
		if <-results {
			accepted++
		}
	}
	if accepted != 3 {
		t.Fatalf("accepted %d concurrent events, want 3", accepted)
	}
	if !limiter.take("second-key") || limiter.take("third-key") {
		t.Fatal("key bound was not enforced")
	}
	limiter.reset("same-key")
	if !limiter.take("third-key") {
		t.Fatal("reset did not release key capacity")
	}
}
