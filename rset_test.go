package g1gc

import "testing"

func TestRememberedSetRetainsAllCrossRegionEdges(t *testing.T) {
	h, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()

	source, err := h.AllocateObject(128, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.Allocate(400); err != nil {
		t.Fatal(err)
	}
	if _, err = h.Allocate(400); err != nil {
		t.Fatal(err)
	}
	targetA, err := h.Allocate(128)
	if err != nil {
		t.Fatal(err)
	}
	targetB, err := h.Allocate(128)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.SetReference(source, 0, targetA); err != nil {
		t.Fatal(err)
	}
	if err := h.SetReference(source, 1, targetB); err != nil {
		t.Fatal(err)
	}
	sourceRegion, err := h.ObjectInfo(source)
	if err != nil {
		t.Fatal(err)
	}
	targetRegion, err := h.ObjectInfo(targetA)
	if err != nil {
		t.Fatal(err)
	}
	remembered, err := h.RememberedSet(targetRegion.Region)
	if err != nil {
		t.Fatal(err)
	}
	if len(remembered) != 1 || remembered[0] != sourceRegion.Region {
		t.Fatalf("remembered set = %v, want source region %d", remembered, sourceRegion.Region)
	}
	if err := h.ClearReference(source, 0); err != nil {
		t.Fatal(err)
	}
	remembered, err = h.RememberedSet(targetRegion.Region)
	if err != nil {
		t.Fatal(err)
	}
	if len(remembered) != 1 || remembered[0] != sourceRegion.Region {
		t.Fatalf("second edge was erased: remembered set = %v", remembered)
	}
	if err := h.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestRSetIndexTracksPairTransitions(t *testing.T) {
	x := newRSetIndex()
	if !x.add(1, 2) {
		t.Fatal("first add of a pair must report new")
	}
	if x.add(1, 2) {
		t.Fatal("second add of a pair must not report new")
	}
	if x.remove(1, 2) {
		t.Fatal("removing one of two edges must not report gone")
	}
	if !x.remove(1, 2) {
		t.Fatal("removing the last edge must report gone")
	}
	if x.remove(1, 2) {
		t.Fatal("removing a missing pair must not report gone")
	}
	x.add(5, 6)
	x.clear()
	if x.remove(5, 6) {
		t.Fatal("clear must drop all pairs")
	}
}
