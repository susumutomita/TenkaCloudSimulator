package guestnet

import "testing"

func TestARPMatchesOnlyTrustedReply(t *testing.T) {
	r, e := NewARPResponder(arpFixtureConfig())
	if e != nil {
		t.Fatal(e)
	}
	expected := arpReplyFixture()
	if !r.MatchesReply(expected) {
		t.Fatal("independent canonical bytes rejected")
	}
	for i := range expected {
		b := append([]byte(nil), expected...)
		b[i] ^= 1
		if r.MatchesReply(b) {
			t.Fatalf("modified byte %d accepted", i)
		}
	}
	for _, b := range [][]byte{nil, expected[:42], append(append([]byte(nil), expected...), 0), arpRequestFixture(arpFixtureConfig())} {
		if r.MatchesReply(b) {
			t.Fatal("noncanonical accepted")
		}
	}
	var absent *ARPResponder
	if absent.MatchesReply(expected) || new(ARPResponder).MatchesReply(expected) {
		t.Fatal("uninitialized responder")
	}
}
