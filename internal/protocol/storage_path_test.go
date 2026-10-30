package protocol_test

import (
	"os"
	"path/filepath"
	"testing"

	"proxyma/internal/protocol"

	"github.com/stretchr/testify/require"
)

func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)

	require.Equal(t, home, protocol.ExpandHome("~"))
	require.Equal(t, filepath.Join(home, "foo", "bar"), protocol.ExpandHome("~/foo/bar"))
	require.Equal(t, "/tmp/foo", protocol.ExpandHome("/tmp/foo"))
}

func TestResolveActiveStoragePrecedence(t *testing.T) {
	t.Run("explicit storage flag takes precedence", func(t *testing.T) {
		t.Setenv(protocol.EnvStorage, "/env/storage")
		got := protocol.ResolveActiveStorage("/flag/storage", true)
		require.Equal(t, "/flag/storage", got)
	})

	t.Run("env storage takes precedence over default", func(t *testing.T) {
		t.Setenv(protocol.EnvStorage, "/env/storage")
		got := protocol.ResolveActiveStorage("/unused", false)
		require.Equal(t, "/env/storage", got)
	})

	t.Run("defaults to user home ~/.proxyma when unset", func(t *testing.T) {
		t.Setenv(protocol.EnvStorage, "")
		tempHome := t.TempDir()
		t.Setenv("HOME", tempHome)

		got := protocol.ResolveActiveStorage("", false)
		require.Equal(t, filepath.Join(tempHome, ".proxyma"), got)
	})
}
