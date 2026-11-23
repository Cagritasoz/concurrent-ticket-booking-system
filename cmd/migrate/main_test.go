package main

import (
	"strings"
	"testing"
)

func TestParseArgs(t *testing.T) {
	tests := []struct {
		args    []string
		want    command
		wantErr string // part of the error message; "" means no error
	}{
		{args: []string{"up"}, want: command{name: "up"}},
		{args: []string{"down"}, want: command{name: "down"}},
		{args: []string{"version"}, want: command{name: "version"}},
		{args: []string{"force", "3"}, want: command{name: "force", version: 3}},
		{args: []string{"force", "0"}, want: command{name: "force", version: 0}},
		{args: nil, wantErr: "usage"},
		{args: []string{"sideways"}, wantErr: `unknown command "sideways"`},
		{args: []string{"up", "2"}, wantErr: "up takes no arguments"},
		{args: []string{"force"}, wantErr: "force needs exactly one version"},
		{args: []string{"force", "-1"}, wantErr: "whole number"},
		{args: []string{"force", "two"}, wantErr: "whole number"},
	}
	for _, tc := range tests {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			got, err := parseArgs(tc.args)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("got error %v, want one containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestRun_FailsWithoutDatabaseURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	err := run(t.Context(), []string{"version"})
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL is required") {
		t.Fatalf("got %v, want an error about DATABASE_URL", err)
	}
}
