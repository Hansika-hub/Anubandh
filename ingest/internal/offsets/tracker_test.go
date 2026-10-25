package offsets

import "testing"

func TestLowWaterMark(t *testing.T) {
	tr := NewTracker()
	for o := int64(10); o <= 13; o++ {
		tr.Seen("t", 0, o)
	}
	if _, _, adv := tr.Done("t", 0, 12); adv {
		t.Fatal("12 finished before 10 and 11: must not commit")
	}
	if _, _, adv := tr.Done("t", 0, 13); adv {
		t.Fatal("still waiting on 10")
	}
	if from, to, adv := tr.Done("t", 0, 10); !adv || from != 10 || to != 10 {
		t.Fatalf("10 done: got %d..%d adv=%v", from, to, adv)
	}
	if from, to, adv := tr.Done("t", 0, 11); !adv || from != 11 || to != 13 {
		t.Fatalf("11 done should release 11..13, got %d..%d adv=%v", from, to, adv)
	}
	if _, _, adv := tr.Done("t", 0, 11); adv {
		t.Fatal("redelivered/duplicate Done must be ignored")
	}
}

func TestPartitionsAreIndependent(t *testing.T) {
	tr := NewTracker()
	tr.Seen("t", 0, 0)
	tr.Seen("t", 1, 100)
	if _, to, adv := tr.Done("t", 1, 100); !adv || to != 100 {
		t.Fatal("partition 1 should advance on its own")
	}
	if _, _, adv := tr.Done("t", 2, 5); adv {
		t.Fatal("unseen partition must be ignored")
	}
}
