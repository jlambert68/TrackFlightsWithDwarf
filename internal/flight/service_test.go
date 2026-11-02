package flight

import (
	"testing"
	"time"
)

func TestLiveFramesProducePosition(t *testing.T) {
	service := NewService()
	if _, err := service.ConsumeLine("8D40621D58C382D690C8AC2863A7"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ConsumeLine("8D40621D58C386435CC412692AD6"); err != nil {
		t.Fatal(err)
	}
	state := service.LiveState()
	if len(state.Aircraft) != 1 || !state.Aircraft[0].PositionReady || state.Aircraft[0].Latitude == nil {
		t.Fatalf("expected a decoded live position: %+v", state)
	}
}

func TestLiveAircraftExpiryChangesImmediately(t *testing.T) {
	service := NewService()
	if _, err := service.ConsumeLine("8D40621D58C382D690C8AC2863A7"); err != nil {
		t.Fatal(err)
	}
	service.mu.Lock()
	entry := service.flights["40621D"]
	entry.LastSeen = time.Now().Add(-30 * time.Second)
	service.flights["40621D"] = entry
	service.mu.Unlock()
	service.SetAircraftExpiry(time.Minute)
	if len(service.Snapshot()) != 1 {
		t.Fatal("aircraft expired before configured timeout")
	}
	service.SetAircraftExpiry(10 * time.Second)
	if len(service.Snapshot()) != 0 {
		t.Fatal("aircraft remained after configured timeout")
	}
}

func TestLiveStateRetainsRecoveredPositionsAndExpiresTracks(t *testing.T) {
	service := NewService()
	now := time.Now().UTC()
	for i, frame := range []string{"8D40621D58C382D690C8AC2863A7", "8D40621D58C386435CC412692AD6"} {
		if _, err := service.ConsumeLineAt(frame, now.Add(time.Duration(i-2)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	state := service.LiveState()
	points := state.Trails["40621D"]
	if len(points) == 0 || points[len(points)-1].Timestamp.After(now) {
		t.Fatalf("missing recovered trail: %+v", points)
	}
	points[0].Latitude = 0
	if service.LiveState().Trails["40621D"][0].Latitude == 0 {
		t.Fatal("snapshot shares mutable trail")
	}
	service.SetAircraftExpiry(time.Millisecond)
	if len(service.LiveState().Trails) != 0 {
		t.Fatal("expired trail remained visible")
	}
}
