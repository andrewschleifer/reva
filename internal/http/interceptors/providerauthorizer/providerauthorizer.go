// Copyright 2018-2020 CERN
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
//
// In applying this license, CERN does not waive the privileges and immunities
// granted to it by virtue of its status as an Intergovernmental Organization
// or submit itself to any jurisdiction.

package providerauthorizer

import (
	"fmt"
	"net/http"
	"strings"

	userpb "github.com/cs3org/go-cs3apis/cs3/identity/user/v1beta1"
	"github.com/cs3org/reva/pkg/appctx"
	"github.com/cs3org/reva/pkg/ocm/provider"
	"github.com/cs3org/reva/pkg/ocm/provider/authorizer/registry"
	"github.com/cs3org/reva/pkg/rgrpc/todo/pool"
	"github.com/cs3org/reva/pkg/rhttp/global"
	"github.com/cs3org/reva/pkg/rhttp/router"
	"github.com/cs3org/reva/pkg/sharedconf"
	"github.com/mitchellh/mapstructure"
)

const (
	defaultPriority = 200
)

func init() {
	global.RegisterMiddleware("providerauthorizer", New)
}

type config struct {
	Driver     string                            `mapstructure:"driver"`
	Drivers    map[string]map[string]interface{} `mapstructure:"drivers"`
	OCMPrefix  string                            `mapstructure:"ocm_prefix"`
	GatewaySvc string
}

func getDriver(c *config) (provider.Authorizer, error) {
	if f, ok := registry.NewFuncs[c.Driver]; ok {
		return f(c.Drivers[c.Driver])
	}

	return nil, fmt.Errorf("driver %s not found for provider authorizer", c.Driver)
}

// New returns a new HTTP middleware that verifies that the provider is registered in OCM.
func New(m map[string]interface{}) (global.Middleware, int, error) {

	conf := &config{}
	if err := mapstructure.Decode(m, conf); err != nil {
		return nil, 0, err
	}

	conf.GatewaySvc = sharedconf.GetGatewaySVC(conf.GatewaySvc)
	if conf.OCMPrefix == "" {
		conf.OCMPrefix = "ocm"
	}

	authorizer, err := getDriver(conf)
	if err != nil {
		return nil, 0, err
	}

	handler := func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

			ctx := r.Context()
			log := appctx.GetLogger(ctx)
			if head, _ := router.ShiftPath(r.URL.Path); head != conf.OCMPrefix {
				log.Info().Msg("skipping provider authorizer check for: " + r.URL.Path)
				h.ServeHTTP(w, r)
				return
			}

			username, _, ok := r.BasicAuth()
			if !ok {
				log.Error().Err(err).Msg("no basic auth provided")
				w.WriteHeader(http.StatusUnauthorized)
				return
			}

			gatewayClient, err := pool.GetGatewayServiceClient(conf.GatewaySvc)
			if err != nil {
				log.Error().Err(err).Msg("error getting the grpc client")
				w.WriteHeader(http.StatusInternalServerError)
				return
			}

			userRes, err := gatewayClient.FindUsers(ctx, &userpb.FindUsersRequest{
				Filter: username,
			})
			if err != nil {
				log.Error().Err(err).Msg("error searching for the user")
				w.WriteHeader(http.StatusInternalServerError)
				return
			}

			var userAuth *userpb.User
			for _, user := range userRes.GetUsers() {
				if user.Username == username {
					userAuth = user
					break
				}
			}
			domainSplit := strings.Split(userAuth.Mail, "@")
			if len(domainSplit) != 2 {
				log.Error().Err(err).Msg("user mail must contain domain")
				w.WriteHeader(http.StatusBadRequest)
				return
			}

			err = authorizer.IsProviderAllowed(ctx, domainSplit[1])
			if err != nil {
				log.Error().Err(err).Msg("provider not registered in OCM")
				w.WriteHeader(http.StatusUnauthorized)
				return
			}

			h.ServeHTTP(w, r)
		})
	}

	return handler, defaultPriority, nil

}
