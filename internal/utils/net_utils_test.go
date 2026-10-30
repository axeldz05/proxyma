package utils_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"proxyma/internal/utils"
	"strings"
	"testing"
	"time"

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

		addrV6, err := utils.ResolveUDPAddrContext(context.Background(), "udp", "[::1]:8080")
		require.NoError(t, err)
		require.Equal(t, "[::1]:8080", addrV6.String())
	})

	t.Run("aborts on context cancelled before resolution", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := utils.ResolveUDPAddrContext(ctx, "udp", "localhost:8080")
		require.ErrorIs(t, err, context.Canceled)
	})

	t.Run("aborts on context timeout during resolution", func(t *testing.T) {
		restore := utils.SetNetResolverForTest(&net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			},
		})
		defer restore()

		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()

		_, err := utils.ResolveUDPAddrContext(ctx, "udp", "some-unresolved-domain.local:8080")
		require.Error(t, err)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.Contains(t, err.Error(), "failed to resolve address")
	})

	t.Run("incompatible network family", func(t *testing.T) {
		// IPv4 literal with udp6
		_, err := utils.ResolveUDPAddrContext(context.Background(), "udp6", "127.0.0.1:8080")
		require.Error(t, err)
		require.Contains(t, err.Error(), "did not resolve to a compatible IP")

		// IPv6 literal with udp4
		_, err = utils.ResolveUDPAddrContext(context.Background(), "udp4", "[::1]:8080")
		require.Error(t, err)
		require.Contains(t, err.Error(), "did not resolve to a compatible IP")
	})

	t.Run("port fallback to service name lookup", func(t *testing.T) {
		addr, err := utils.ResolveUDPAddrContext(context.Background(), "udp", "127.0.0.1:domain")
		require.NoError(t, err)
		require.Equal(t, 53, addr.Port)

		_, err = utils.ResolveUDPAddrContext(context.Background(), "udp", "127.0.0.1:notarealservicenamexyz")
		require.Error(t, err)
		require.Contains(t, err.Error(), "failed to resolve UDP port")
	})

	t.Run("invalid address format", func(t *testing.T) {
		_, err := utils.ResolveUDPAddrContext(context.Background(), "udp", "not-a-valid-addr")
		require.Error(t, err)
	})
}

func TestHTTPStatusErrors(t *testing.T) {
	t.Parallel()
	err := utils.HTTPStatusError(http.StatusNotFound)
	require.Error(t, err)
	require.Contains(t, err.Error(), "404")

	respOK := &http.Response{
		StatusCode: http.StatusOK,
	}
	require.NoError(t, utils.HTTPErrorFromResponse(respOK, "test"))

	respErr := &http.Response{
		StatusCode: http.StatusBadGateway,
		Body:       io.NopCloser(strings.NewReader("gateway down")),
	}
	errResp := utils.HTTPErrorFromResponse(respErr, "peer")
	require.Error(t, errResp)
	require.Contains(t, errResp.Error(), "peer returned status 502: gateway down")
}

func TestReadLimits(t *testing.T) {
	tempDir := t.TempDir()
	restore := utils.SetCgroupPathsForTest(
		filepath.Join(tempDir, "cpu.max.none"),
		filepath.Join(tempDir, "cpu.cfs_quota_us.none"),
		filepath.Join(tempDir, "cpu.cfs_period_us.none"),
		filepath.Join(tempDir, "memory.max.none"),
		filepath.Join(tempDir, "memory.limit_in_bytes.none"),
	)
	defer restore()

	require.Equal(t, float64(0), utils.ReadCPULimit())
	require.Equal(t, int64(0), utils.ReadMemoryLimit())

	// Cgroups v2
	v2CPU := filepath.Join(tempDir, "cpu.max")
	require.NoError(t, os.WriteFile(v2CPU, []byte("250000 100000\n"), 0644))
	v2Mem := filepath.Join(tempDir, "memory.max")
	require.NoError(t, os.WriteFile(v2Mem, []byte("1073741824\n"), 0644))

	utils.SetCgroupPathsForTest(v2CPU, "", "", v2Mem, "")
	require.InDelta(t, 2.5, utils.ReadCPULimit(), 0.001)
	require.Equal(t, int64(1073741824), utils.ReadMemoryLimit())

	// cgroups v2 "max" (unlimited)
	require.NoError(t, os.WriteFile(v2CPU, []byte("max 100000\n"), 0644))
	require.NoError(t, os.WriteFile(v2Mem, []byte("max\n"), 0644))
	require.Equal(t, float64(0), utils.ReadCPULimit())
	require.Equal(t, int64(0), utils.ReadMemoryLimit())

	// Cgroups v1 fallback (when v2 files are absent)
	require.NoError(t, os.Remove(v2CPU))
	require.NoError(t, os.Remove(v2Mem))

	v1Quota := filepath.Join(tempDir, "cpu.cfs_quota_us")
	v1Period := filepath.Join(tempDir, "cpu.cfs_period_us")
	v1Mem := filepath.Join(tempDir, "memory.limit_in_bytes")

	require.NoError(t, os.WriteFile(v1Quota, []byte("150000\n"), 0644))
	require.NoError(t, os.WriteFile(v1Period, []byte("100000\n"), 0644))
	require.NoError(t, os.WriteFile(v1Mem, []byte("2147483648\n"), 0644))

	utils.SetCgroupPathsForTest("", v1Quota, v1Period, "", v1Mem)
	require.InDelta(t, 1.5, utils.ReadCPULimit(), 0.001)
	require.Equal(t, int64(2147483648), utils.ReadMemoryLimit())

	// cgroups v1 unlimited (-1 or huge value)
	require.NoError(t, os.WriteFile(v1Quota, []byte("-1\n"), 0644))
	require.NoError(t, os.WriteFile(v1Mem, []byte("9223372036854771712\n"), 0644))
	require.Equal(t, float64(0), utils.ReadCPULimit())
	require.Equal(t, int64(0), utils.ReadMemoryLimit())
}
