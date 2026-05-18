// Copyright 2018 Palantir Technologies, Inc.
// Copyright 2020 G-Research Limited
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package server

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/c2h5oh/datasize"
	"github.com/die-net/lrucache"
	"github.com/gregjones/httpcache"
	"github.com/palantir/go-baseapp/baseapp"
	"github.com/palantir/go-baseapp/appmetrics/emitter/datadog"
	"github.com/palantir/go-githubapp/githubapp"
	"github.com/palantir/policy-bot/pull"
	"github.com/pkg/errors"
	"github.com/rs/zerolog"
	"goji.io/pat"

	"github.com/G-Research/tfe-plan-bot/plan"
	"github.com/G-Research/tfe-plan-bot/server/handler"
	"github.com/G-Research/tfe-plan-bot/version"
)

const (
	DefaultGitHubTimeout = 10 * time.Second

	DefaultWebhookWorkers   = 10
	DefaultWebhookQueueSize = 100

	DefaultHTTPCacheSize     = 50 * datasize.MB
	DefaultPushedAtCacheSize = 100_000
)

type Server struct {
	config *Config
	base   *baseapp.Server
}

// New instantiates a new Server.
// Callers must then invoke Start to run the Server.
func New(c *Config) (*Server, error) {
	logger := baseapp.NewLogger(baseapp.LoggingConfig{
		Level:  c.Logging.Level,
		Pretty: c.Logging.Text,
	})

	base, err := baseapp.NewServer(c.Server, baseapp.DefaultParams(logger, "tfe-plan-bot.")...)
	if err != nil {
		return nil, errors.Wrap(err, "failed to initialize base server")
	}

	maxSize := int64(DefaultHTTPCacheSize)
	if c.Cache.MaxSize != 0 {
		maxSize = int64(c.Cache.MaxSize)
	}

	githubTimeout := c.Workers.GithubTimeout
	if githubTimeout == 0 {
		githubTimeout = 10 * time.Second
	}

	userAgent := fmt.Sprintf("tfe-plan-bot/%s", version.GetVersion())
	cc, err := githubapp.NewDefaultCachingClientCreator(
		c.Github,
		githubapp.WithClientUserAgent(userAgent),
		githubapp.WithClientTimeout(githubTimeout),
		githubapp.WithClientCaching(true, func() httpcache.Cache {
			return lrucache.New(maxSize, 0)
		}),
		githubapp.WithClientMiddleware(
			githubapp.ClientLogging(zerolog.DebugLevel),
			githubapp.ClientMetrics(base.Registry()),
		),
	)
	if err != nil {
		return nil, errors.Wrap(err, "failed to initialize client creator")
	}

	appClient, err := cc.NewAppClient()
	if err != nil {
		return nil, errors.Wrap(err, "failed to initialize Github app client")
	}

	app, _, err := appClient.Apps.Get(context.Background(), "")
	if err != nil {
		return nil, errors.Wrap(err, "failed to get configured GitHub app")
	}

	pushedAtSize := c.Cache.PushedAtSize
	if pushedAtSize == 0 {
		pushedAtSize = DefaultPushedAtCacheSize
	}

	globalCache, err := pull.NewLRUGlobalCache(pushedAtSize)
	if err != nil {
		return nil, errors.Wrap(err, "failed to initialize global cache")
	}

	// TODO(jgiannuzzi) use ClientLogging with a different message than github_request
	tp, err := plan.NewClientProvider(
		c.TFE,
		githubapp.ClientLogging(zerolog.DebugLevel),
		githubapp.ClientMetrics(base.Registry()),
	)
	if err != nil {
		return nil, errors.Wrap(err, "failed to initialize TFE client provider")
	}

	httpClient := &http.Client{
		Transport: githubapp.ClientLogging(zerolog.DebugLevel)(
			githubapp.ClientMetrics(base.Registry())(
				http.DefaultTransport,
			),
		),
	}

	basePolicyHandler := handler.Base{
		ClientCreator:     cc,
		BaseConfig:        &c.Server,
		Installations:     githubapp.NewInstallationsService(appClient),
		GlobalCache:       globalCache,
		TFEClientProvider: tp,
		HTTPClient:        httpClient,

		PullOpts: &c.Options,
		ConfigFetcher: &handler.ConfigFetcher{
			PolicyPath: c.Options.ConfigPath,
		},

		AppName: app.GetSlug(),
	}

	queueSize := c.Workers.QueueSize
	if queueSize < 1 {
		queueSize = DefaultWebhookQueueSize
	}

	workers := c.Workers.Workers
	if workers < 1 {
		workers = DefaultWebhookWorkers
	}

	dispatcher := githubapp.NewEventDispatcher(
		[]githubapp.EventHandler{
			&handler.PullRequest{Base: basePolicyHandler},
			&handler.PullRequestReview{Base: basePolicyHandler},
			&handler.IssueComment{Base: basePolicyHandler},
			&handler.Status{Base: basePolicyHandler},
			&handler.CheckRun{Base: basePolicyHandler},
		},
		c.Github.App.WebhookSecret,
		githubapp.WithErrorCallback(githubapp.MetricsErrorCallback(base.Registry())),
		githubapp.WithScheduler(
			githubapp.QueueAsyncScheduler(
				queueSize, workers,
				githubapp.WithSchedulingMetrics(base.Registry()),
				githubapp.WithAsyncErrorCallback(githubapp.MetricsAsyncErrorCallback(base.Registry())),
			),
		),
	)

	mux := base.Mux()

	// webhook route
	mux.Handle(pat.Post(githubapp.DefaultWebhookRoute), dispatcher)

	// additional API routes
	mux.Handle(pat.Get("/api/health"), handler.Health())

	return &Server{
		config: c,
		base:   base,
	}, nil
}

// Start is blocking and long-running
func (s *Server) Start() error {
	if s.config.Datadog.Address != "" {
		if err := datadog.StartEmitter(s.base, s.config.Datadog); err != nil {
			return err
		}
	}
	return s.base.Start()
}
