package g1gc

import "testing"

func testPoolRegions() []*region {
	return []*region{
		{id: 0, kind: RegionFree, capacity: 1024},
		{id: 1, kind: RegionFree, capacity: 1024},
		{id: 2, kind: RegionFree, capacity: 512},
	}
}

func TestFreePoolPushPopClaim(t *testing.T) {
	regions := testPoolRegions()
	p := newFreePool(len(regions))
	p.push(0, 1024)
	p.push(1, 1024)
	p.push(2, 512)
	p.push(1, 1024)
	if p.free != 2560 {
		t.Fatalf("free = %d, want 2560 (duplicate push must be idempotent)", p.free)
	}
	if r := p.pop(regions, nil); r != regions[2] {
		t.Fatalf("LIFO pop = %v, want region 2", r)
	}
	if r := p.pop(regions, map[RegionID]bool{1: true}); r != regions[0] {
		t.Fatalf("excluded-top pop = %v, want region 0", r)
	}
	if r := p.pop(regions, map[RegionID]bool{1: true}); r != nil {
		t.Fatalf("fully excluded pop = %v, want nil", r)
	}
	if !p.claim(regions, 1) {
		t.Fatal("claim of a free region must succeed")
	}
	if p.claim(regions, 1) {
		t.Fatal("claim of a claimed region must fail")
	}
	if p.claim(regions, 99) {
		t.Fatal("claim of an invalid index must fail")
	}
	if p.free != 0 {
		t.Fatalf("free = %d, want 0", p.free)
	}
	p.reset()
	p.push(0, 1024)
	if r := p.pop(regions, nil); r != regions[0] {
		t.Fatalf("post-reset pop = %v, want region 0", r)
	}
}

func TestActiveCacheSetClearGet(t *testing.T) {
	var a activeCache
	a.reset()
	if _, ok := a.get(RegionEden); ok {
		t.Fatal("fresh cache must miss")
	}
	a.set(RegionEden, 3)
	id, ok := a.get(RegionEden)
	if !ok || id != 3 {
		t.Fatalf("get = %d,%v, want 3,true", id, ok)
	}
	a.set(RegionKind(99), 1)
	if _, ok := a.get(RegionKind(99)); ok {
		t.Fatal("out-of-range kind must miss")
	}
	a.set(RegionOld, 3)
	a.clear(3)
	if _, ok := a.get(RegionEden); ok {
		t.Fatal("clear must drop every kind pointing at the region")
	}
	if _, ok := a.get(RegionOld); ok {
		t.Fatal("clear must drop every kind pointing at the region")
	}
}

func TestAllocatorTakeActiveAndUsed(t *testing.T) {
	regions := []*region{
		{id: 0, kind: RegionEden, capacity: 1024, used: 900},
		{id: 1, kind: RegionFree, capacity: 1024},
	}
	a := newAllocator(len(regions))
	a.setActive(RegionEden, 0)
	if r := a.takeActive(regions, RegionEden, 100, nil); r != regions[0] {
		t.Fatalf("takeActive = %v, want region 0", r)
	}
	if r := a.takeActive(regions, RegionEden, 200, nil); r != nil {
		t.Fatalf("takeActive without slack = %v, want nil", r)
	}
	if r := a.takeActive(regions, RegionEden, 100, map[RegionID]bool{0: true}); r != nil {
		t.Fatalf("takeActive excluded = %v, want nil", r)
	}
	regions[0].kind = RegionOld
	if r := a.takeActive(regions, RegionEden, 100, nil); r != nil {
		t.Fatalf("takeActive of converted region = %v, want nil", r)
	}
	a.addUsed(512)
	a.addUsed(128)
	a.subUsed(512)
	if a.usedBytes() != 128 {
		t.Fatalf("used = %d, want 128", a.usedBytes())
	}
}
