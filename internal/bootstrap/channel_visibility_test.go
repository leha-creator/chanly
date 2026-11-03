package bootstrap

import (
	"testing"

	"github.com/leha-creator/chanly/internal/channel/domain"
)

func TestChannelVisibilityEnvironment(t *testing.T) {
	for _, environment := range []string{EnvDevelopment, EnvProduction, EnvTest, "", "staging"} {
		cfg := Config{Env: environment}
		allowed := cfg.channelVisibilityPolicy() == domain.AllowPrivateChannels
		if allowed != (environment == EnvDevelopment) {
			t.Fatalf("private channel policy for %q: %v", environment, allowed)
		}
	}
}
