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

package plan

import (
	"net/http"
	"os"
	"strings"

	"github.com/hashicorp/go-tfe"
	"github.com/palantir/go-githubapp/githubapp"
	"github.com/pkg/errors"
)

type ClientProviderConfig struct {
	Address       string               `yaml:"address"`
	Organizations []OrganizationConfig `yaml:"organizations"`
}

type OrganizationConfig struct {
	Name  string `yaml:"name"`
	Token string `yaml:"token"`
}

func (c *ClientProviderConfig) SetValuesFromEnv() {
	if v, ok := os.LookupEnv("TFE_ADDRESS"); ok {
		c.Address = v
	}

	if orgs, ok := os.LookupEnv("TFE_ORGANIZATIONS"); ok {
		for _, org := range strings.Split(strings.TrimRight(orgs, " \t\r\n"), ",") {
			split := strings.SplitN(org, ":", 2)
			c.Organizations = append(c.Organizations, OrganizationConfig{
				Name:  split[0],
				Token: split[1],
			})
		}
	}
}

type ClientProvider struct {
	config  ClientProviderConfig
	clients map[string]*tfe.Client
}

func NewClientProvider(c ClientProviderConfig, middlewares ...githubapp.ClientMiddleware) (*ClientProvider, error) {
	p := &ClientProvider{
		config:  c,
		clients: make(map[string]*tfe.Client),
	}

	transport := http.DefaultTransport
	for _, middleware := range middlewares {
		transport = middleware(transport)
	}

	for _, org := range c.Organizations {
		client, err := tfe.NewClient(&tfe.Config{
			Address: c.Address,
			Token:   org.Token,
			HTTPClient: &http.Client{
				Transport: transport,
			},
		})
		if err != nil {
			return nil, err
		}
		p.clients[org.Name] = client
	}

	return p, nil
}

func (p *ClientProvider) Address() string {
	return strings.TrimSuffix(p.config.Address, "/")
}

func (p *ClientProvider) Client(org string) (*tfe.Client, error) {
	c := p.clients[org]
	if c == nil {
		return c, errors.Errorf("no token configured for org %q", org)
	}
	return c, nil
}
