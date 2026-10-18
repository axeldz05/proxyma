package utils_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"proxyma/internal/utils"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStripURLScheme(t *testing.T) {
	t.Parallel()
	require.Equal(t, "host:8443", utils.StripURLScheme("https://host:8443"))
	require.Equal(t, "host:8080", utils.StripURLScheme("http://host:8080"))
	require.Equal(t, "bare", utils.StripURLScheme("bare"))
}

func TestFileExists(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.False(t, utils.FileExists(""))
	require.False(t, utils.FileExists(filepath.Join(dir, "missing")))
	require.False(t, utils.FileExists(dir)) // directory

	f := filepath.Join(dir, "f.txt")
	require.NoError(t, os.WriteFile(f, []byte("x"), 0o644))
	require.True(t, utils.FileExists(f))
}

func TestClientHost(t *testing.T) {
	t.Parallel()
	require.Equal(t, "10.0.0.1", utils.ClientHost("10.0.0.1:443"))
	require.Equal(t, "barehost", utils.ClientHost("barehost"))
}

func TestHTTPSuccess(t *testing.T) {
	t.Parallel()
	require.True(t, utils.HTTPSuccess(200))
	require.True(t, utils.HTTPSuccess(204))
	require.False(t, utils.HTTPSuccess(199))
	require.False(t, utils.HTTPSuccess(300))
	require.False(t, utils.HTTPSuccess(404))
}

func TestExtractPort(t *testing.T) {
	t.Parallel()
	require.Equal(t, "8443", utils.ExtractPort("https://127.0.0.1:8443"))
	require.Equal(t, "8080", utils.ExtractPort("localhost:8080"))
	require.Equal(t, "", utils.ExtractPort("no-port"))
}

func TestIsLoopbackHost(t *testing.T) {
	t.Parallel()
	require.True(t, utils.IsLoopbackHost("localhost:8080"))
	require.True(t, utils.IsLoopbackHost("https://127.0.0.1:8443"))
	require.True(t, utils.IsLoopbackHost("::1"))
	require.True(t, utils.IsLoopbackHost("myhost")) // bare hostname without dots
	require.False(t, utils.IsLoopbackHost("example.com:443"))
	require.False(t, utils.IsLoopbackHost("https://10.0.0.5:8443"))
}

func TestGenerateDefaultNodeID(t *testing.T) {
	t.Parallel()
	id := utils.GenerateDefaultNodeID()
	require.NotEmpty(t, id)
	require.Contains(t, id, "-")
}

func TestGetLocalIPs(t *testing.T) {
	t.Parallel()
	ips, err := utils.GetLocalIPs()
	require.NoError(t, err)
	routable, err := utils.GetRoutableLocalIPs()
	require.NoError(t, err)
	require.LessOrEqual(t, len(routable), len(ips))
}

func TestSameIPFamily(t *testing.T) {
	t.Parallel()
	require.True(t, utils.SameIPFamily(net.IPv4(127, 0, 0, 1), "udp4"))
	require.False(t, utils.SameIPFamily(net.ParseIP("::1"), "udp4"))
	require.True(t, utils.SameIPFamily(net.ParseIP("::1"), "udp6"))
	require.False(t, utils.SameIPFamily(net.IPv4(127, 0, 0, 1), "udp6"))
}

func TestResolveUDPAddrContext(t *testing.T) {
	t.Parallel()

	t.Run("resolves IP literal", func(t *testing.T) {
		addr, err := utils.ResolveUDPAddrContext(context.Background(), "udp", "127.0.0.1:8080")
		require.NoError(t, err)
		require.Equal(t, "127.0.0.1:8080", addr.String())
	})

	t.Run("aborts on cancelled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := utils.ResolveUDPAddrContext(ctx, "udp", "localhost:8080")
		require.ErrorIs(t, err, context.Canceled)
	})

	t.Run("invalid address format", func(t *testing.T) {
		_, err := utils.ResolveUDPAddrContext(context.Background(), "udp", "not-a-valid-addr")
		require.Error(t, err)
	})
}
