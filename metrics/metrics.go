package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	GroupsPerParentDomainTotal = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "groups_per_domain_updates_total",
			Help: "Total number of shard updates",
		},
		[]string{"parent_domain", "destination"},
	)
	SourcesPerParentDomainTotal = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "sources_per_domain_updates_total",
			Help: "Total number of sources per group per parent domain",
		},
		[]string{"parent_domain", "destination"},
	)
	SourcePerGroupUpdatesTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "sources_per_group_updates_total",
			Help: "Total number of sources per group per parent domain",
		},
		[]string{"source", "group_id", "parent_domain"},
	)

	ParentDomainUpdatesTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "domainbinding_parent_domain_updates_total",
			Help: "Total number of parent domain updates",
		},
		[]string{"parent_domain", "destination"},
	)
)

func Init() {
	// Register all metrics here
	metrics.Registry.MustRegister(
		GroupsPerParentDomainTotal,
		SourcesPerParentDomainTotal,
		SourcePerGroupUpdatesTotal,
		ParentDomainUpdatesTotal,
	)
}
