package bot

import "testing"

func TestRateLimiterBurstThenReject(t *testing.T) {
	r := newRateLimiter()
	for i := 0; i < 3; i++ {
		ok, _ := r.allow("u1", 6, 3)
		if !ok {
			t.Fatalf("burst request %d rejected", i)
		}
	}
	ok, first := r.allow("u1", 6, 3)
	if ok {
		t.Fatal("4th request should be rejected")
	}
	if !first {
		t.Fatal("first rejection should be flagged for a notice")
	}
	ok, first = r.allow("u1", 6, 3)
	if ok || first {
		t.Fatal("subsequent rejections should be silent")
	}
}

func TestRateLimiterPerUser(t *testing.T) {
	r := newRateLimiter()
	for i := 0; i < 3; i++ {
		r.allow("u1", 6, 3)
	}
	if ok, _ := r.allow("u2", 6, 3); !ok {
		t.Fatal("u2 should be unaffected by u1's bucket")
	}
}

func TestRateLimiterDisabled(t *testing.T) {
	r := newRateLimiter()
	for i := 0; i < 100; i++ {
		if ok, _ := r.allow("u1", 0, 0); !ok {
			t.Fatal("perMinute=0 should disable limiting")
		}
	}
}
