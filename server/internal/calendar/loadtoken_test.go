package calendar_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"calendar-display/internal/calendar"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadToken_MalformedJSON(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "token.json")
	require.NoError(t, os.WriteFile(path, []byte(`{not valid json`), 0600))

	_, err := calendar.LoadToken(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode token")
}

func TestLoadToken_WorldReadable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file permission bits not enforced on Windows")
	}
	t.Parallel()
	path := filepath.Join(t.TempDir(), "token.json")
	require.NoError(t, os.WriteFile(path, []byte(`{}`), 0644))

	_, err := calendar.LoadToken(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "insecure permissions")
}
