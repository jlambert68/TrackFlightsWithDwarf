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
