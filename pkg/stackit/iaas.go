package stackit

import (
	"os"

	"github.com/stackitcloud/stackit-sdk-go/core/config"
)

func ClientOptions() []config.ConfigurationOption {
	opts := []config.ConfigurationOption{}
	if url := os.Getenv("IAAS_API_URL"); url != "" {
		opts = append(opts, config.WithEndpoint(url))
	}
	return opts
}
