package proto

import "testing"

func TestPopupPathLevel(t *testing.T) {
	if Since(OpenPopup{}) != PopupLevel || Since(OpenPopup{Path: "x"}) != PopupPathLevel {
		t.Fatal("path popups need Level 60; other popups need Level 54")
	}
}
