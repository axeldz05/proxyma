package compute_test

import (
	"strings"
	"sync"
	"testing"

	"proxyma/internal/compute"
	"proxyma/internal/protocol"

	"github.com/stretchr/testify/require"
)

func validEchoService(name string) protocol.LocalService {
	return protocol.LocalService{
		Type: protocol.ServiceTypeExec,
		Exec: "/bin/echo",
		Schema: protocol.ServiceSchema{
			Name: name,
			Type: protocol.ServiceTypeExec,
		},
	}
}

func TestDeleteLocalServicePersistsAndRejectsUnknown(t *testing.T) {
	t.Parallel()
	storagePath := t.TempDir()

	require.NoError(t, compute.UpsertLocalService(storagePath, "lab.echo", validEchoService("lab.echo")))

	err := compute.DeleteLocalService(storagePath, "lab.missing")
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")

	services, err := compute.LoadServicesMap(storagePath)
	require.NoError(t, err)
	require.Len(t, services, 1, "failed delete must not remove anything")

	require.NoError(t, compute.DeleteLocalService(storagePath, "lab.echo"))

	services, err = compute.LoadServicesMap(storagePath)
	require.NoError(t, err)
	require.NotContains(t, services, "lab.echo", "deletion must persist to services.json")
}

func TestConcurrentDeleteLocalServiceKeepsJSONValid(t *testing.T) {
	t.Parallel()
	storagePath := t.TempDir()

	require.NoError(t, compute.UpsertLocalService(storagePath, "lab.echo", validEchoService("lab.echo")))
	require.NoError(t, compute.UpsertLocalService(storagePath, "lab.other", validEchoService("lab.other")))

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			errs[slot] = compute.DeleteLocalService(storagePath, "lab.echo")
		}(i)
	}
	wg.Wait()

	succeeded := 0
	for _, err := range errs {
		if err == nil {
			succeeded++
		} else {
			require.Contains(t, err.Error(), "not found")
		}
	}
	require.Equal(t, 1, succeeded, "exactly one concurrent delete may win")

	services, err := compute.LoadServicesMap(storagePath)
	require.NoError(t, err, "services.json must stay parseable after concurrent deletes")
	require.NotContains(t, services, "lab.echo")
	require.Contains(t, services, "lab.other")
	require.True(t, strings.HasSuffix(compute.ServicesFilePath(storagePath), "services.json"))
}
