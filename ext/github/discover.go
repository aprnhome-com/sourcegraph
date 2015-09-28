package github

import (
	"errors"
	"fmt"
	"strings"

	"golang.org/x/net/context"
	"gopkg.in/inconshreveable/log15.v2"
	"sourcegraph.com/sourcegraph/go-sourcegraph/sourcegraph"
	"sourcegraph.com/sourcegraph/sourcegraph/fed"
	"sourcegraph.com/sourcegraph/sourcegraph/fed/discover"
	"sourcegraph.com/sourcegraph/sourcegraph/server/local"
	"sourcegraph.com/sourcegraph/sourcegraph/store"
	"sourcegraph.com/sourcegraph/sourcegraph/svc"
	"sourcegraph.com/sourcegraph/sourcegraph/svc/middleware/remote"
)

func init() {
	discover.QuickRepoFuncs = append(discover.QuickRepoFuncs, discoverRepo)
}

// discoverRepo implements the discovery process for a repo that might
// be hosted on GitHub. If it is not hosted on GitHub, a
// discover.NotFoundError is returned.
//
// TODO(sqs): add support for GitHub Enterprise by actually fetching
// and inspecting the page.
func discoverRepo(ctx context.Context, repo string) (discover.Info, error) {
	if strings.HasPrefix(strings.ToLower(repo), "github.com/") {
		return &discoveryInfo{host: "github.com"}, nil
	}
	return nil, &discover.NotFoundError{Type: "repo", Input: repo}
}

type discoveryInfo struct {
	host string // GitHub hostname (always "github.com" for now)
}

func (i *discoveryInfo) NewContext(ctx context.Context) (context.Context, error) {
	if !fed.Config.IsRoot {
		rootGRPCEndpoint, err := fed.Config.RootGRPCEndpoint()
		if err != nil {
			log15.Error("GitHub repo discovery could not locate the mothership", "error", err)
			return nil, err
		}
		if rootGRPCEndpoint == nil {
			return nil, errors.New("federation root URL not configured")
		}
		log15.Debug("Routing external repo request to root", "RootGRPCEndpoint", rootGRPCEndpoint.String())
		ctx = sourcegraph.WithGRPCEndpoint(ctx, rootGRPCEndpoint)
		return svc.WithServices(ctx, remote.Services), nil
	} else {
		log15.Debug("Serving GitHub repo request locally")
		ctx = store.WithRepos(ctx, &Repos{})
		return svc.WithServices(ctx, local.Services), nil
	}
}

func (i *discoveryInfo) String() string { return fmt.Sprintf("GitHub (%s)", i.host) }
