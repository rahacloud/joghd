package alerter

import (
	"context"

	"github.com/rahacloud/joghd/internal/domain"
)

// CompanyFilter wraps an Alerter and only forwards alerts whose target
// company matches one of the configured companies. An empty list is
// treated as a catch-all.
type CompanyFilter struct {
	inner     Alerter
	companies map[string]struct{}
	catchAll  bool
}

// NewCompanyFilter wraps inner with a company allow-list.
func NewCompanyFilter(inner Alerter, companies []string) *CompanyFilter {
	set := make(map[string]struct{}, len(companies))
	for _, c := range companies {
		set[c] = struct{}{}
	}

	return &CompanyFilter{
		inner:     inner,
		companies: set,
		catchAll:  len(set) == 0,
	}
}

// Send forwards the alert to the wrapped alerter when the company matches.
func (f *CompanyFilter) Send(ctx context.Context, alert domain.Alert) error {
	if !f.catchAll {
		if _, ok := f.companies[alert.Target.Company]; !ok {
			return nil
		}
	}

	return f.inner.Send(ctx, alert)
}

// Name returns the wrapped alerter's name.
func (f *CompanyFilter) Name() string {
	return f.inner.Name()
}
