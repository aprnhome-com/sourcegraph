package search

import "sourcegraph.com/sourcegraph/go-sourcegraph/sourcegraph"

func tp(tok sourcegraph.Token) *sourcegraph.PBToken {
	pbtok := sourcegraph.PBTokenWrap(tok)
	return &pbtok
}
