package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExtractParams(t *testing.T) {
	tests := []struct {
		name       string
		argv       []string
		wantParams map[string]string
		wantRest   []string
	}{
		{
			name:       "no params",
			argv:       []string{"build", "Logistics", "--wait"},
			wantParams: nil,
			wantRest:   []string{"build", "Logistics", "--wait"},
		},
		{
			name:       "single param",
			argv:       []string{"build", "Logistics", "--param-branch=main"},
			wantParams: map[string]string{"branch": "main"},
			wantRest:   []string{"build", "Logistics"},
		},
		{
			name:       "multiple params interleaved with other args",
			argv:       []string{"build", "--param-branch=feature/x", "Logistics", "--param-env=uat1", "--wait"},
			wantParams: map[string]string{"branch": "feature/x", "env": "uat1"},
			wantRest:   []string{"build", "Logistics", "--wait"},
		},
		{
			name:       "quoted value preserved verbatim",
			argv:       []string{"--param-message=hello world"},
			wantParams: map[string]string{"message": "hello world"},
			wantRest:   []string{},
		},
		{
			name:       "equals inside value splits on first",
			argv:       []string{"--param-query=a=b=c"},
			wantParams: map[string]string{"query": "a=b=c"},
			wantRest:   []string{},
		},
		{
			name:       "empty value is kept",
			argv:       []string{"--param-tag="},
			wantParams: map[string]string{"tag": ""},
			wantRest:   []string{},
		},
		{
			name:       "bare prefix without equals passes through",
			argv:       []string{"--param-branch", "main"},
			wantParams: nil,
			wantRest:   []string{"--param-branch", "main"},
		},
		{
			name:       "empty name passes through",
			argv:       []string{"--param-=value"},
			wantParams: nil,
			wantRest:   []string{"--param-=value"},
		},
		{
			name:       "non-param flags untouched",
			argv:       []string{"--profile", "work", "--json", "list"},
			wantParams: nil,
			wantRest:   []string{"--profile", "work", "--json", "list"},
		},
		{
			name:       "later duplicate wins",
			argv:       []string{"--param-x=1", "--param-x=2"},
			wantParams: map[string]string{"x": "2"},
			wantRest:   []string{},
		},
		{
			name:       "double-dash terminator passes param-shaped positionals through",
			argv:       []string{"build", "job", "--param-real=1", "--", "--param-literal=2"},
			wantParams: map[string]string{"real": "1"},
			wantRest:   []string{"build", "job", "--", "--param-literal=2"},
		},
		{
			name:       "empty argv",
			argv:       nil,
			wantParams: nil,
			wantRest:   []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params, rest := extractParams(tt.argv)
			assert.Equal(t, tt.wantParams, params)
			assert.Equal(t, tt.wantRest, rest)
		})
	}
}

func TestHasVersionFlag(t *testing.T) {
	tests := []struct {
		name string
		argv []string
		want bool
	}{
		{"present alone", []string{"--version"}, true},
		{"present before a command", []string{"--version", "list"}, true},
		{"absent", []string{"list", "--json"}, false},
		{"empty", nil, false},
		{"after the -- terminator does not count", []string{"build", "--", "--version"}, false},
		{"not an exact-token match", []string{"--param-x=--version"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, hasVersionFlag(tt.argv))
		})
	}
}
