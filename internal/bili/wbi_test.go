package bili

import (
	"strconv"
	"testing"
	"time"
)

func TestSignStable(t *testing.T) {
	s, err := NewWBISigner(
		"7cd084941338484aae1ad9425b84077c",
		"4932caff0ff746eab6f01bf08b70ac45",
	)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Sign(map[string]string{"foo": "114", "bar": "514", "wts": "1700000000"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["w_rid"]; !ok {
		t.Fatal("missing w_rid")
	}
	if len(got["w_rid"]) != 32 {
		t.Fatalf("w_rid wrong length: %s", got["w_rid"])
	}
}

func TestSignKnownAnswer(t *testing.T) {
	s, err := NewWBISigner(
		"7cd084941338484aae1ad9425b84077c",
		"4932caff0ff746eab6f01bf08b70ac45",
	)
	if err != nil {
		t.Fatal(err)
	}
	if s.mixinKey != "ea1db124af3c7062474693fa704f4ff8" {
		t.Fatalf("mixinKey = %s, want ea1db124af3c7062474693fa704f4ff8", s.mixinKey)
	}

	in := map[string]string{"foo": "114", "bar": "514", "wts": "1700000000"}
	got, err := s.Sign(in)
	if err != nil {
		t.Fatal(err)
	}
	if got["w_rid"] != "b087ee77bc14a2c3f19eb6ab9d167c5f" {
		t.Fatalf("w_rid = %s, want b087ee77bc14a2c3f19eb6ab9d167c5f", got["w_rid"])
	}
	if got["foo"] != "114" || got["bar"] != "514" || got["wts"] != "1700000000" {
		t.Fatalf("params not preserved: %v", got)
	}
	if _, mutated := in["w_rid"]; mutated {
		t.Fatal("Sign mutated the caller's map")
	}
}

func TestSignAddsWTSWhenAbsent(t *testing.T) {
	s, err := NewWBISigner(
		"7cd084941338484aae1ad9425b84077c",
		"4932caff0ff746eab6f01bf08b70ac45",
	)
	if err != nil {
		t.Fatal(err)
	}
	before := time.Now().Unix()
	got, err := s.Sign(map[string]string{"foo": "114"})
	if err != nil {
		t.Fatal(err)
	}
	wts, err := strconv.ParseInt(got["wts"], 10, 64)
	if err != nil {
		t.Fatalf("wts not a unix seconds string: %q (%v)", got["wts"], err)
	}
	if wts < before || wts > time.Now().Unix() {
		t.Fatalf("wts %d outside [%d, %d]", wts, before, time.Now().Unix())
	}
}

func TestNewWBISignerRejectsShortKey(t *testing.T) {
	if _, err := NewWBISigner("tooshort", "alsoshort"); err == nil {
		t.Fatal("expected error for keys shorter than 64 characters")
	}
}
