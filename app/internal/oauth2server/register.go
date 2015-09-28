package oauth2server

import (
	"errors"
	"net/http"

	"github.com/google/go-querystring/query"

	"sourcegraph.com/sourcegraph/go-sourcegraph/sourcegraph"
	"sourcegraph.com/sourcegraph/sourcegraph/app/internal"
	"sourcegraph.com/sourcegraph/sourcegraph/app/internal/schemautil"
	"sourcegraph.com/sourcegraph/sourcegraph/app/internal/tmpl"
	"sourcegraph.com/sourcegraph/sourcegraph/app/router"
	"sourcegraph.com/sourcegraph/sourcegraph/pkg/oauth2util"
	"sourcegraph.com/sourcegraph/sourcegraph/util/handlerutil"
	"sourcegraph.com/sourcegraph/sourcegraph/util/httputil/httpctx"
)

func init() {
	internal.Handlers[router.RegisterClient] = serveRegisterClient
}

func serveRegisterClient(w http.ResponseWriter, r *http.Request) error {
	cl := handlerutil.APIClient(r)
	ctx := httpctx.FromRequest(r)

	var opt oauth2util.AuthorizeParams
	if err := schemautil.Decode(&opt, r.URL.Query()); err != nil {
		return err
	}

	if opt.ClientID == "" {
		return &handlerutil.HTTPErr{Status: http.StatusBadRequest, Err: errors.New("no ClientID in URL")}
	}
	if opt.JWKS == "" {
		return &handlerutil.HTTPErr{Status: http.StatusBadRequest, Err: errors.New("no JWKS in URL")}
	}
	if opt.RedirectURI == "" {
		return &handlerutil.HTTPErr{Status: http.StatusBadRequest, Err: errors.New("no RedirectURI in URL")}
	}

	if err := r.ParseForm(); err != nil {
		return err
	}
	var form struct{ ClientName string }
	if err := schemautil.Decode(&form, r.PostForm); err != nil {
		return err
	}

	regClient := &sourcegraph.RegisteredClient{
		ID:           opt.ClientID,
		ClientName:   form.ClientName,
		JWKS:         opt.JWKS,
		RedirectURIs: []string{opt.RedirectURI},
	}

	if r.Method == "GET" {
		return tmpl.Exec(r, w, "client-registration/register-client.html", http.StatusOK, nil, &struct {
			tmpl.Common
		}{})
	}

	regClient, err := cl.RegisteredClients.Create(ctx, regClient)
	if err != nil {
		return err
	}

	// Redirect back to the authorize screen, now that the client has
	// been registered.
	u := router.Rel.URLTo(router.OAuth2ServerAuthorize)
	q, err := query.Values(opt)
	if err != nil {
		return err
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusSeeOther)
	return nil
}
