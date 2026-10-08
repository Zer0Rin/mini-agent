package store_test

import (
	"testing"

	"github.com/Zer0Rin/mini-agent/internal/store"
	"github.com/Zer0Rin/mini-agent/internal/store/storetest"
)

func TestMemoryContract(t *testing.T) {
	storetest.Run(t, func(*testing.T) store.Store { return store.NewMemory() })
}
