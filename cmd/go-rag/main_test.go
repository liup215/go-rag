package main

import (
	"reflect"
	"testing"
)

func TestResolveListOffset(t *testing.T) {
	tests := []struct {
		name       string
		limit      int
		offset     int
		page       int
		wantOffset int
		wantErr    bool
	}{
		{name: "defaults", limit: 100, offset: 0, page: 0, wantOffset: 0},
		{name: "offset only", limit: 100, offset: 50, page: 0, wantOffset: 50},
		{name: "page overrides offset", limit: 100, offset: 50, page: 2, wantOffset: 100},
		{name: "page three with small limit", limit: 50, offset: 0, page: 3, wantOffset: 100},
		{name: "page one is offset zero", limit: 100, offset: 25, page: 1, wantOffset: 0},
		{name: "no limit with offset", limit: 0, offset: 10, page: 0, wantOffset: 10},
		{name: "negative limit", limit: -1, offset: 0, page: 0, wantErr: true},
		{name: "negative offset", limit: 100, offset: -1, page: 0, wantErr: true},
		{name: "negative page", limit: 100, offset: 0, page: -1, wantErr: true},
		{name: "page with limit zero", limit: 0, offset: 0, page: 2, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveListOffset(tt.limit, tt.offset, tt.page)
			if (err != nil) != tt.wantErr {
				t.Fatalf("resolveListOffset(%d, %d, %d) error = %v, wantErr %v", tt.limit, tt.offset, tt.page, err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if got != tt.wantOffset {
				t.Fatalf("resolveListOffset(%d, %d, %d) = %d, want %d", tt.limit, tt.offset, tt.page, got, tt.wantOffset)
			}
		})
	}
}

func TestFilterFlagsSet(t *testing.T) {
	t.Run("single filter", func(t *testing.T) {
		f := filterFlags{}
		if err := f.Set("status=indexed"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := map[string][]string{"status": {"indexed"}}
		if !reflect.DeepEqual(map[string][]string(f), want) {
			t.Fatalf("got %v, want %v", map[string][]string(f), want)
		}
	})

	t.Run("repeated key accumulates values", func(t *testing.T) {
		f := filterFlags{}
		for _, v := range []string{"status=indexed", "status=failed", "type=pdf"} {
			if err := f.Set(v); err != nil {
				t.Fatalf("unexpected error for %q: %v", v, err)
			}
		}
		want := map[string][]string{"status": {"indexed", "failed"}, "type": {"pdf"}}
		if !reflect.DeepEqual(map[string][]string(f), want) {
			t.Fatalf("got %v, want %v", map[string][]string(f), want)
		}
	})

	t.Run("whitespace is trimmed", func(t *testing.T) {
		f := filterFlags{}
		if err := f.Set(" status = indexed "); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := map[string][]string{"status": {"indexed"}}
		if !reflect.DeepEqual(map[string][]string(f), want) {
			t.Fatalf("got %v, want %v", map[string][]string(f), want)
		}
	})

	t.Run("value may contain equals", func(t *testing.T) {
		f := filterFlags{}
		if err := f.Set("name=a=b.txt"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := map[string][]string{"name": {"a=b.txt"}}
		if !reflect.DeepEqual(map[string][]string(f), want) {
			t.Fatalf("got %v, want %v", map[string][]string(f), want)
		}
	})

	invalid := []string{"status", "=indexed", "status=", "  =  "}
	for _, v := range invalid {
		t.Run("rejects "+v, func(t *testing.T) {
			f := filterFlags{}
			if err := f.Set(v); err == nil {
				t.Fatalf("expected error for %q", v)
			}
		})
	}
}

func TestFilterFlagsString(t *testing.T) {
	if got := (filterFlags{}).String(); got != "" {
		t.Fatalf("empty filters should render as empty string, got %q", got)
	}

	f := filterFlags{"status": {"indexed"}, "type": {"pdf"}}
	// Keys must be sorted so the output is deterministic.
	if got, want := f.String(), "status=indexed, type=pdf"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
