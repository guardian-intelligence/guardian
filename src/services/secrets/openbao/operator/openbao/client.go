package openbao

import (
	"fmt"
	"os"

	baoapi "github.com/openbao/openbao/api/v2"
)

func NewClientFromEnv() (*baoapi.Client, error) {
	address := os.Getenv("OPENBAO_ADDR")
	if address == "" {
		return nil, fmt.Errorf("OPENBAO_ADDR is required")
	}
	cfg := baoapi.DefaultConfig()
	cfg.Address = address
	return baoapi.NewClient(cfg)
}
