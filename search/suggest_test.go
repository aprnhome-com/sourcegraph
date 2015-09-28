package search

import (
	"fmt"
	"reflect"
	"testing"

	"golang.org/x/net/context"

	"sort"

	"sourcegraph.com/sourcegraph/go-sourcegraph/sourcegraph"
	"sourcegraph.com/sourcegraph/go-sourcegraph/sourcegraph/mock"
	"sourcegraph.com/sourcegraph/sourcegraph/auth"
	"sourcegraph.com/sourcegraph/sourcegraph/svc"
	"sourcegraph.com/sourcegraph/srclib/graph"
	"sourcegraph.com/sourcegraph/srclib/unit"
)

func TestSuggest(t *testing.T) {
	origBuiltinOrgs := builtinOrgs
	builtinOrgs = nil
	defer func() {
		builtinOrgs = origBuiltinOrgs
	}()

	tests := []struct {
		query   []sourcegraph.Token
		want    []*sourcegraph.Suggestion
		wantErr error

		mockReposList              func(ctx context.Context, opt *sourcegraph.RepoListOptions) (*sourcegraph.RepoList, error)
		mockReposGet               func(ctx context.Context, repoSpec *sourcegraph.RepoSpec) (*sourcegraph.Repo, error)
		mockBuildsGetRepoBuildInfo func(ctx context.Context, op *sourcegraph.BuildsGetRepoBuildInfoOp) (*sourcegraph.RepoBuildInfo, error)
		mockOrgsList               func(ctx context.Context, op *sourcegraph.OrgsListOp) (*sourcegraph.OrgList, error)
		mockDefsList               func(ctx context.Context, opt *sourcegraph.DefListOptions) (*sourcegraph.DefList, error)
		mockUnitsGet               func(ctx context.Context, unitSpec *sourcegraph.UnitSpec) (*unit.RepoSourceUnit, error)
		mockUnitsList              func(ctx context.Context, opt *sourcegraph.UnitListOptions) (*sourcegraph.RepoSourceUnitList, error)
	}{
		{
			query: []sourcegraph.Token{},
			want: []*sourcegraph.Suggestion{
				{
					Query: sourcegraph.PBTokensWrap([]sourcegraph.Token{sourcegraph.RepoToken{URI: "r"}, sourcegraph.Term("d2")}),
				},
				{
					Query: sourcegraph.PBTokensWrap([]sourcegraph.Token{sourcegraph.RepoToken{URI: "r"}, sourcegraph.Term("d2")}),
				},
				{
					Query: sourcegraph.PBTokensWrap([]sourcegraph.Token{sourcegraph.UserToken{Login: "u"}, sourcegraph.Term("d1")}),
				},
				{
					Query: sourcegraph.PBTokensWrap([]sourcegraph.Token{sourcegraph.UserToken{Login: "o"}, sourcegraph.Term("d1")}),
				},
			},
			mockReposList: func(ctx context.Context, opt *sourcegraph.RepoListOptions) (*sourcegraph.RepoList, error) {
				return &sourcegraph.RepoList{Repos: []*sourcegraph.Repo{{URI: "r"}}}, nil
			},
			mockBuildsGetRepoBuildInfo: reposGetBuildOK,
			mockOrgsList: func(ctx context.Context, op *sourcegraph.OrgsListOp) (*sourcegraph.OrgList, error) {
				return &sourcegraph.OrgList{Orgs: []*sourcegraph.Org{{User: sourcegraph.User{Login: "o", IsOrganization: true}}}}, nil
			},
			mockDefsList: func(ctx context.Context, opt *sourcegraph.DefListOptions) (*sourcegraph.DefList, error) {
				return &sourcegraph.DefList{
					Defs: []*sourcegraph.Def{
						{Def: graph.Def{Name: "d1"}},
						{Def: graph.Def{Name: "d2"}},
					},
				}, nil
			},
		},
	}
	for _, test := range tests {
		label := "<< " + debugFormatTokens(test.query) + " >> "

		ctx := svc.WithServices(context.Background(), svc.Services{
			Repos: &mock.ReposServer{
				Get_:  test.mockReposGet,
				List_: test.mockReposList,
			},
			Builds: &mock.BuildsServer{
				GetRepoBuildInfo_: test.mockBuildsGetRepoBuildInfo,
			},
			Orgs: &mock.OrgsServer{
				List_: test.mockOrgsList,
			},
			Defs: &mock.DefsServer{
				List_: test.mockDefsList,
			},
		})
		ctx = auth.WithActor(ctx, auth.Actor{UID: 1, Login: "u"})

		suggs, err := Suggest(ctx, test.query, SuggestionConfig{})
		if !reflect.DeepEqual(err, test.wantErr) {
			if test.wantErr == nil {
				t.Errorf("%s: Suggest: %s", label, err)
			} else {
				t.Errorf("%s: Suggest: got error %q, want %q", label, err, test.wantErr)
			}
			continue
		}
		if err != nil {
			continue
		}

		sort.Sort(suggestions(suggs))
		sort.Sort(suggestions(test.want))
		for _, sugg := range suggs {
			stripTokenObjects(sugg.Query)
			sugg.Description = ""
		}

		if !reflect.DeepEqual(suggs, test.want) {
			t.Errorf("%s: got suggestions\n%s\n\nwant\n%s", label, asJSON(suggs), asJSON(test.want))
		}
	}
}

func stripTokenObjects(tokens []sourcegraph.PBToken) {
	for i, pbtok := range tokens {
		switch tok := pbtok.Token().(type) {
		case sourcegraph.RepoToken:
			tok.Repo = nil
			pbtok.RepoToken = &tok
			tokens[i] = pbtok
		case sourcegraph.UnitToken:
			tok.Unit = nil
			pbtok.UnitToken = &tok
			tokens[i] = pbtok
		case sourcegraph.RevToken:
			tok.Commit = nil
			pbtok.RevToken = &tok
			tokens[i] = pbtok
		case sourcegraph.FileToken:
			tok.Entry = nil
			pbtok.FileToken = &tok
			tokens[i] = pbtok
		case sourcegraph.UserToken:
			tok.User = nil
			pbtok.UserToken = &tok
			tokens[i] = pbtok
		case sourcegraph.Term:
		case sourcegraph.AnyToken:
		default:
			panic(fmt.Sprintf("unrecognized token type %T", pbtok.Token()))
		}
	}
}
