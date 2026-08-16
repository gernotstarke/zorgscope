package memstore

import (
	"testing"

	"github.com/gernotstarke/zorgscope/internal/ports"
	"github.com/gernotstarke/zorgscope/internal/ports/storetest"
)

func TestContract(t *testing.T) {
	storetest.Run(t, func(t *testing.T) ports.Store { return New() })
}
