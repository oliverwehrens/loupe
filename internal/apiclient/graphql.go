package apiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// GraphQLRequest is the standard GraphQL request envelope.
type GraphQLRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables,omitempty"`
}

// GraphQLResponse is the standard GraphQL response envelope.
type GraphQLResponse struct {
	Data   json.RawMessage `json:"data"`
	Errors []GraphQLError  `json:"errors,omitempty"`
}

// GraphQLError represents a single GraphQL error.
type GraphQLError struct {
	Message string `json:"message"`
}

// DoGraphQL posts a GraphQL request and returns the raw `data` field. Any
// `errors` array in the response is surfaced as a Go error so callers don't
// have to inspect a 200-with-errors envelope themselves.
func (c *Client) DoGraphQL(ctx context.Context, path, query string, variables map[string]any) (json.RawMessage, error) {
	reqBody, err := json.Marshal(GraphQLRequest{Query: query, Variables: variables})
	if err != nil {
		return nil, fmt.Errorf("%s graphql: marshal request: %w", c.displayName(), err)
	}

	resp, err := c.Do(ctx, "POST", path, "", bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%s graphql: read response: %w", c.displayName(), err)
	}
	if int64(len(body)) > MaxResponseBodyBytes {
		return nil, fmt.Errorf("%s graphql: response body exceeds %d bytes", c.displayName(), MaxResponseBodyBytes)
	}

	var gqlResp GraphQLResponse
	if err := json.Unmarshal(body, &gqlResp); err != nil {
		return nil, fmt.Errorf("%s graphql: decode response: %w", c.displayName(), err)
	}

	if len(gqlResp.Errors) > 0 {
		msgs := make([]string, len(gqlResp.Errors))
		for i, e := range gqlResp.Errors {
			msgs[i] = e.Message
		}
		return nil, fmt.Errorf("%s graphql error: %s", c.displayName(), strings.Join(msgs, "; "))
	}

	return gqlResp.Data, nil
}
