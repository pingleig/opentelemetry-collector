// Copyright The OpenTelemetry Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//       http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package prometheusreceiver

import (
	"context"
	"time"

	"github.com/prometheus/prometheus/discovery"
	"github.com/prometheus/prometheus/pkg/relabel"
	"github.com/prometheus/prometheus/scrape"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/receiver/prometheusreceiver/internal"
)

// pReceiver is the type that provides Prometheus scraper/receiver functionality.
type pReceiver struct {
	cfg        *Config
	consumer   consumer.MetricsConsumer
	cancelFunc context.CancelFunc

	logger *zap.Logger
}

// New creates a new prometheus.Receiver reference.
func newPrometheusReceiver(logger *zap.Logger, cfg *Config, next consumer.MetricsConsumer) *pReceiver {
	pr := &pReceiver{
		cfg:      cfg,
		consumer: next,
		logger:   logger,
	}
	return pr
}

// Start is the method that starts Prometheus scraping and it
// is controlled by having previously defined a Configuration using perhaps New.
func (r *pReceiver) Start(ctx context.Context, host component.Host) error {
	discoveryCtx, cancel := context.WithCancel(context.Background())
	r.cancelFunc = cancel

	logger := internal.NewZapToGokitLogAdapter(r.logger)

	// A hack for https://github.com/open-telemetry/opentelemetry-collector/issues/575#issuecomment-797719376
	for i := range r.cfg.PrometheusConfig.ScrapeConfigs {
		r.cfg.PrometheusConfig.ScrapeConfigs[i].RelabelConfigs = append(r.cfg.PrometheusConfig.ScrapeConfigs[i].RelabelConfigs, &relabel.Config{
			Action:      relabel.Replace,
			Regex:       relabel.MustNewRegexp("^instance$"),             // instance is always there, so we will find a match
			Replacement: r.cfg.PrometheusConfig.ScrapeConfigs[i].JobName, // value is job name
			TargetLabel: internal.MagicScrapeJobLabel,                    // creates a new magic label
		})
	}

	discoveryManager := discovery.NewManager(discoveryCtx, logger)
	discoveryCfg := make(map[string]discovery.Configs)
	for _, scrapeConfig := range r.cfg.PrometheusConfig.ScrapeConfigs {
		discoveryCfg[scrapeConfig.JobName] = scrapeConfig.ServiceDiscoveryConfigs
	}
	if err := discoveryManager.ApplyConfig(discoveryCfg); err != nil {
		return err
	}
	go func() {
		if err := discoveryManager.Run(); err != nil {
			r.logger.Error("Discovery manager failed", zap.Error(err))
			host.ReportFatalError(err)
		}
	}()

	var jobsMap *internal.JobsMap
	if !r.cfg.UseStartTimeMetric {
		jobsMap = internal.NewJobsMap(2 * time.Minute)
	}
	ocaStore := internal.NewOcaStore(ctx, r.consumer, r.logger, jobsMap, r.cfg.UseStartTimeMetric, r.cfg.StartTimeMetricRegex, r.cfg.Name())

	scrapeManager := scrape.NewManager(logger, ocaStore)
	ocaStore.SetScrapeManager(scrapeManager)
	if err := scrapeManager.ApplyConfig(r.cfg.PrometheusConfig); err != nil {
		return err
	}
	go func() {
		if err := scrapeManager.Run(discoveryManager.SyncCh()); err != nil {
			r.logger.Error("Scrape manager failed", zap.Error(err))
			host.ReportFatalError(err)
		}
	}()
	return nil
}

// Shutdown stops and cancels the underlying Prometheus scrapers.
func (r *pReceiver) Shutdown(context.Context) error {
	r.cancelFunc()
	return nil
}
