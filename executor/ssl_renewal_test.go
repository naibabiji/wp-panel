package executor

import (
	"testing"

	"github.com/naibabiji/wp-panel/models"
)

func TestSSLAutoRenewalEligibility(t *testing.T) {
	for name, tc := range map[string]struct {
		site *models.Website
		want bool
	}{
		"automatic active certificate": {site: &models.Website{Status: models.StatusActive, SSLCertSource: "auto"}, want: true},
		"paused website":               {site: &models.Website{Status: models.StatusPaused, SSLCertSource: "auto"}, want: false},
		"manual certificate":           {site: &models.Website{Status: models.StatusActive, SSLCertSource: "manual"}, want: false},
		"unknown legacy source":        {site: &models.Website{Status: models.StatusActive}, want: false},
		"missing website":              {site: nil, want: false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := sslAutoRenewalEligible(tc.site); got != tc.want {
				t.Fatalf("eligible=%v, want %v", got, tc.want)
			}
		})
	}
}
