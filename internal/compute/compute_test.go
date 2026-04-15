package compute_test

import (
	"proxyma/internal/compute"
	"proxyma/internal/protocol"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNodeCannotRegisterDuplicateServices(t *testing.T) {
    registry := compute.NewServiceRegistry() 
    
    schema := protocol.ServiceSchema{ Name: "ocr" }
    
    err1 := registry.Register(schema)
    err2 := registry.Register(schema)
    
    require.NoError(t, err1)
    require.ErrorIs(t, err2, compute.ErrServiceDuplicate)
}
