package session

import (
	"testing"

	"go.mau.fi/whatsmeow/store"
)

func TestWAVersionNewer(t *testing.T) {
	tests := []struct {
		a, b store.WAVersionContainer
		want bool
	}{
		{
			a:    store.WAVersionContainer{2, 3000, 1050728965},
			b:    store.WAVersionContainer{2, 3000, 1050728964},
			want: true,
		},
		{
			a:    store.WAVersionContainer{2, 3000, 1050728964},
			b:    store.WAVersionContainer{2, 3000, 1050728964},
			want: false,
		},
		{
			a:    store.WAVersionContainer{2, 3000, 1050728963},
			b:    store.WAVersionContainer{2, 3000, 1050728964},
			want: false,
		},
		{
			a:    store.WAVersionContainer{2, 3001, 0},
			b:    store.WAVersionContainer{2, 3000, 999999999},
			want: true,
		},
		{
			a:    store.WAVersionContainer{3, 0, 0},
			b:    store.WAVersionContainer{2, 9999, 999999999},
			want: true,
		},
	}
	for _, tc := range tests {
		got := waVersionNewer(tc.a, tc.b)
		if got != tc.want {
			t.Errorf("waVersionNewer(%v, %v) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}
