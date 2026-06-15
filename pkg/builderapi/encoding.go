package builderapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	gloastypes "github.com/ethpandaops/buildoor/pkg/builderapi/gloas/types"
)

// Wire formats accepted on Gloas builder-API request bodies. The builder-spec
// permits either JSON or SSZ, selected via the Content-Type header.
const (
	contentTypeJSON = "application/json"
	contentTypeSSZ  = "application/octet-stream"
)

// errUnsupportedContentType lets handlers map an unknown Content-Type to a 415
// response, distinct from a decode failure (which is a 400).
var errUnsupportedContentType = errors.New("unsupported content type")

// normalizeContentType lowercases the media type and drops any parameters
// (e.g. "application/json; charset=utf-8" -> "application/json").
func normalizeContentType(ct string) string {
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	return strings.ToLower(strings.TrimSpace(ct))
}

// parseSignedRequestAuth decodes a SignedRequestAuthV1 from JSON or SSZ selected
// by contentType. The Content-Type must be set explicitly to one of the two
// supported media types; an empty or unrecognized type returns
// errUnsupportedContentType (which handlers map to 415).
func parseSignedRequestAuth(data []byte, contentType string) (*gloastypes.SignedRequestAuthV1, error) {
	var v gloastypes.SignedRequestAuthV1
	switch normalizeContentType(contentType) {
	case contentTypeSSZ:
		if err := v.UnmarshalSSZ(data); err != nil {
			return nil, fmt.Errorf("invalid SSZ SignedRequestAuthV1: %w", err)
		}
	case contentTypeJSON:
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, fmt.Errorf("invalid SignedRequestAuthV1: %w", err)
		}
	default:
		return nil, errUnsupportedContentType
	}
	return &v, nil
}

// parseBuilderPreferencesRequest decodes a BuilderPreferencesRequestV1 from JSON
// or SSZ selected by contentType, following the same rules as
// parseSignedRequestAuth.
func parseBuilderPreferencesRequest(data []byte, contentType string) (*gloastypes.BuilderPreferencesRequestV1, error) {
	var v gloastypes.BuilderPreferencesRequestV1
	switch normalizeContentType(contentType) {
	case contentTypeSSZ:
		if err := v.UnmarshalSSZ(data); err != nil {
			return nil, fmt.Errorf("invalid SSZ BuilderPreferencesRequestV1: %w", err)
		}
	case contentTypeJSON:
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, fmt.Errorf("invalid BuilderPreferencesRequestV1: %w", err)
		}
	default:
		return nil, errUnsupportedContentType
	}
	return &v, nil
}
