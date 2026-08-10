package server

import (
	"testing"

	"proxyma/shared/uischema"
)

func TestUnixHandlersMatchRegistry(t *testing.T) {
	reg := uischema.AllUnixActions()
	handlers := RegisteredUnixActions()

	for ua, key := range reg {
		if _, ok := handlers[ua]; !ok {
			t.Errorf("registry UnixAction %q (%s) has no daemon handler", ua, key)
		}
	}
	for ua := range handlers {
		if _, ok := reg[ua]; !ok {
			t.Errorf("daemon handler %q is not declared in uischema.Registry", ua)
		}
	}
}
