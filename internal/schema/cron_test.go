package schema

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateCron(t *testing.T) {
	tests := []struct {
		name    string
		cron    map[string]string
		funcs   []string
		wantErr []string
	}{
		{"valid", map[string]string{"sync-gdevm-docs": "0 * * * *"}, []string{"sync-gdevm-docs"}, nil},
		{"absent", nil, nil, nil},
		{"function from devm.me.sh", map[string]string{"mine": "0 3 * * *"}, []string{"mine"}, nil},
		{"function missing", map[string]string{"nope": "0 * * * *"}, []string{"other"}, []string{"cron.nope:", "not defined in devm.sh"}},
		{"install reserved", map[string]string{"install": "0 * * * *"}, []string{"install"}, []string{"cron.install:", "runs automatically"}},
		{"startup reserved", map[string]string{"startup": "0 * * * *"}, []string{"startup"}, []string{"cron.startup:", "runs automatically"}},
		{"macro rejected", map[string]string{"sync-gdevm-docs": "@hourly"}, []string{"sync-gdevm-docs"}, []string{"cron.sync-gdevm-docs:"}},
		{"four fields rejected", map[string]string{"sync-gdevm-docs": "0 * * *"}, []string{"sync-gdevm-docs"}, []string{"cron.sync-gdevm-docs:"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := Config{Cron: tc.cron, Functions: tc.funcs}.validateCron()
			if tc.wantErr == nil {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			for _, s := range tc.wantErr {
				assert.Contains(t, err.Error(), s)
			}
		})
	}
}
