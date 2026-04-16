package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// NewServer builds a fully-configured MCP server with all tasks127 tools registered.
// The caller decides how to expose it (stdio, Streamable HTTP, etc.) via sdk.Server.Run.
func NewServer(client *Client) *sdk.Server {
	s := sdk.NewServer(&sdk.Implementation{
		Name:    "tasks127",
		Version: "0.1.0",
	}, nil)
	registerTools(s, client)
	return s
}

// registerTools wires every MCP tool handler into the server. Tools are kept
// deliberately coarse-grained and workflow-oriented; see README.md and the
// MCP best-practices sources referenced during design. There are 15 of them.
func registerTools(s *sdk.Server, c *Client) {
	// --- identity and discovery ------------------------------------------------

	sdk.AddTool(s, &sdk.Tool{
		Name: "whoami",
		Description: "Return information about the currently authenticated API key. " +
			"Shows tier (admin or user), associated user_id if any, and the on-behalf-of " +
			"user if one is active. Use this once at the start of a session to confirm " +
			"auth works and to see what you are authorized to do.",
	}, toolWhoami(c))

	sdk.AddTool(s, &sdk.Tool{
		Name: "list_teams",
		Description: "List teams visible to the caller. Returns each team's id, three-letter " +
			"key (e.g. ENG), and name. Call this first if you need a team_id or key when " +
			"creating tickets, projects, or subscriptions.",
	}, toolListTeams(c))

	sdk.AddTool(s, &sdk.Tool{
		Name: "list_projects",
		Description: "List projects visible to the caller. Optionally filter by team. Returns " +
			"each project's id, three-letter key, team_id, and name. Projects are " +
			"subdivisions within a team used for organizing tickets.",
	}, toolListProjects(c))

	sdk.AddTool(s, &sdk.Tool{
		Name: "search_users",
		Description: "Find users by name or email substring. Useful when you need a user_id " +
			"for ticket assignment, commenting as someone, or adding someone to a team.",
	}, toolSearchUsers(c))

	// --- tickets ---------------------------------------------------------------

	sdk.AddTool(s, &sdk.Tool{
		Name: "create_ticket",
		Description: "Create a new ticket. team is required and accepts either a team_id (ULID) " +
			"or a three-letter team key like ENG. project is optional and accepts either a " +
			"project_id or a three-letter project key. If the ticket has a project the " +
			"display ID uses the project's key (e.g. BAK-14); otherwise it uses the team's key. " +
			"parent is optional and makes this ticket a sub-ticket; remember that the two-level " +
			"limit means the parent must itself be a top-level ticket.",
	}, toolCreateTicket(c))

	sdk.AddTool(s, &sdk.Tool{
		Name: "get_ticket",
		Description: "Get a ticket by id. Accepts either a ULID or a display ID like FOO-14. " +
			"Returns all ticket fields. To also see the ticket's comments, use list_comments.",
	}, toolGetTicket(c))

	sdk.AddTool(s, &sdk.Tool{
		Name: "search_tickets",
		Description: `Search tickets using a filter. The filter is a JSON object where keys are ` +
			`field names. Equality is a bare value: {"status":"open"}. Use operator objects for ` +
			`richer comparisons: {"status":{"in":["open","in_progress"]}}, {"title":{"contains":"bug"}}, ` +
			`{"created_at":{"gte":"2026-04-01T00:00:00Z"}}. Supported operators: eq, ne, gt, gte, ` +
			`lt, lte, in, nin, contains, is_null. For OR use $or with an array of sub-filters. ` +
			`Filterable fields: id, key, number, team_id, project_id, parent_id, title, description, ` +
			`status, assignee_user_id, created_at, updated_at.`,
	}, toolSearchTickets(c))

	sdk.AddTool(s, &sdk.Tool{
		Name: "update_tickets",
		Description: "Update one or many tickets. For a single ticket pass ticket (id or display " +
			"ID). For bulk, pass where (a filter, same grammar as search_tickets) and every ticket " +
			"matching it will be updated. Exactly one of ticket or where must be set. " +
			"changes is a map of fields to new values; allowed fields are title, description, status, " +
			"project_id, assignee_user_id. Status must be one of: open, in_progress, blocked, done, canceled. " +
			"Bulk updates cannot change parent_id (the two-level rule requires per-ticket validation).",
	}, toolUpdateTickets(c))

	sdk.AddTool(s, &sdk.Tool{
		Name: "delete_tickets",
		Description: "Soft-delete one or many tickets. For a single ticket pass ticket (id or display " +
			"ID). For bulk, pass where (a filter). Deletion is reversible via the REST API's restore " +
			"endpoint; deleted tickets are simply hidden from default queries.",
	}, toolDeleteTickets(c))

	// --- comments --------------------------------------------------------------

	sdk.AddTool(s, &sdk.Tool{
		Name: "add_comment",
		Description: "Add a comment to a ticket. ticket accepts either a ULID or a display ID " +
			"(e.g. FOO-14). If you are authenticated as a user, the comment is authored as you; " +
			"if you are an unscoped admin, pass author_user_id to specify who the comment is from.",
	}, toolAddComment(c))

	sdk.AddTool(s, &sdk.Tool{
		Name: "list_comments",
		Description: "List comments on a specific ticket. ticket accepts ULID or display ID.",
	}, toolListComments(c))

	// --- subscriptions ---------------------------------------------------------

	sdk.AddTool(s, &sdk.Tool{
		Name: "watch",
		Description: `Register a subscription to be notified of events. resource is the thing to ` +
			`watch ("tickets", "comments", "projects", "teams", "team_members", or "users"). ` +
			`event_types is an array of actions to match ("create", "update", "delete", "restore"). ` +
			`where is a filter using the same grammar as search_tickets, evaluated against each ` +
			`affected row individually (so a bulk update touching 50 tickets only fires if your ` +
			`filter matches). max_fires is optional; set it to 1 for a one-time watch like ` +
			`"tell me the first time X happens". expires_at is optional RFC3339. ` +
			`Events accumulate in the subscription's inbox and are read via read_events.`,
	}, toolWatch(c))

	sdk.AddTool(s, &sdk.Tool{
		Name:        "unwatch",
		Description: "Cancel a subscription. Any events already delivered to the inbox remain readable.",
	}, toolUnwatch(c))

	sdk.AddTool(s, &sdk.Tool{
		Name: "read_events",
		Description: "Read pending events on a subscription. after is the sequence number of the " +
			"last event you already processed (start with 0). Events arrive in order. This call " +
			"does not acknowledge them; call ack_events when you have processed a batch.",
	}, toolReadEvents(c))

	sdk.AddTool(s, &sdk.Tool{
		Name: "ack_events",
		Description: "Acknowledge processed events up to and including cursor. Events with " +
			"sequence <= cursor are dropped from the inbox so they will not be returned again.",
	}, toolAckEvents(c))
}

// --- tool handlers --------------------------------------------------------
//
// Each tool is a function returning the SDK's handler shape. Handlers make a
// REST call via the client and format the response as a TextContent block
// containing pretty-printed JSON. Errors (including APIError) propagate
// unchanged; the SDK will convert them into an MCP tool-error response.

type noArgs struct{}

func toolWhoami(c *Client) sdk.ToolHandlerFor[noArgs, any] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, _ noArgs) (*sdk.CallToolResult, any, error) {
		var out any
		if err := c.get(ctx, "/v1/whoami", &out); err != nil {
			return nil, nil, err
		}
		return asJSON(out), nil, nil
	}
}

type listTeamsArgs struct {
	Limit int `json:"limit,omitempty" jsonschema:"maximum teams to return (default 50, max 200)"`
}

func toolListTeams(c *Client) sdk.ToolHandlerFor[listTeamsArgs, any] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, a listTeamsArgs) (*sdk.CallToolResult, any, error) {
		body := map[string]any{}
		if a.Limit > 0 {
			body["limit"] = a.Limit
		}
		var out any
		if err := c.post(ctx, "/v1/teams/search", body, &out); err != nil {
			return nil, nil, err
		}
		return asJSON(out), nil, nil
	}
}

type listProjectsArgs struct {
	Team  string `json:"team,omitempty" jsonschema:"optional team_id (ULID) or three-letter key to filter to one team's projects"`
	Limit int    `json:"limit,omitempty" jsonschema:"maximum projects to return"`
}

func toolListProjects(c *Client) sdk.ToolHandlerFor[listProjectsArgs, any] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, a listProjectsArgs) (*sdk.CallToolResult, any, error) {
		body := map[string]any{}
		if a.Team != "" {
			teamID, err := c.resolveTeamID(ctx, a.Team)
			if err != nil {
				return nil, nil, err
			}
			body["where"] = map[string]any{"team_id": teamID}
		}
		if a.Limit > 0 {
			body["limit"] = a.Limit
		}
		var out any
		if err := c.post(ctx, "/v1/projects/search", body, &out); err != nil {
			return nil, nil, err
		}
		return asJSON(out), nil, nil
	}
}

type searchUsersArgs struct {
	Contains string `json:"contains,omitempty" jsonschema:"substring to match against name or email (case-insensitive)"`
	Limit    int    `json:"limit,omitempty"`
}

func toolSearchUsers(c *Client) sdk.ToolHandlerFor[searchUsersArgs, any] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, a searchUsersArgs) (*sdk.CallToolResult, any, error) {
		body := map[string]any{}
		if a.Contains != "" {
			// Match on either name or email substring.
			body["where"] = map[string]any{
				"$or": []any{
					map[string]any{"name": map[string]any{"contains": a.Contains}},
					map[string]any{"email": map[string]any{"contains": a.Contains}},
				},
			}
		}
		if a.Limit > 0 {
			body["limit"] = a.Limit
		}
		var out any
		if err := c.post(ctx, "/v1/users/search", body, &out); err != nil {
			return nil, nil, err
		}
		return asJSON(out), nil, nil
	}
}

type createTicketArgs struct {
	Team           string  `json:"team" jsonschema:"team_id (ULID) or three-letter team key; required"`
	Title          string  `json:"title" jsonschema:"ticket title; required"`
	Description    string  `json:"description,omitempty"`
	Project        string  `json:"project,omitempty" jsonschema:"optional project_id or three-letter project key"`
	Parent         string  `json:"parent,omitempty" jsonschema:"optional parent ticket id or display ID (e.g. FOO-14); makes this a sub-ticket"`
	Status         string  `json:"status,omitempty" jsonschema:"one of: open, in_progress, blocked, done, canceled (default: open)"`
	AssigneeUserID *string `json:"assignee_user_id,omitempty"`
}

func toolCreateTicket(c *Client) sdk.ToolHandlerFor[createTicketArgs, any] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, a createTicketArgs) (*sdk.CallToolResult, any, error) {
		if a.Team == "" || a.Title == "" {
			return nil, nil, fmt.Errorf("team and title are required")
		}
		teamID, err := c.resolveTeamID(ctx, a.Team)
		if err != nil {
			return nil, nil, err
		}
		body := map[string]any{
			"team_id": teamID,
			"title":   a.Title,
		}
		if a.Description != "" {
			body["description"] = a.Description
		}
		if a.Project != "" {
			projectID, err := c.resolveProjectID(ctx, a.Project)
			if err != nil {
				return nil, nil, err
			}
			body["project_id"] = projectID
		}
		if a.Parent != "" {
			// Parent can be a display ID; the REST API resolves it for us on read,
			// but the create payload needs a raw ticket id. We look it up first.
			var parent struct {
				ID string `json:"id"`
			}
			if err := c.get(ctx, "/v1/tickets/"+escapePathSeg(a.Parent), &parent); err != nil {
				return nil, nil, fmt.Errorf("resolve parent %q: %w", a.Parent, err)
			}
			body["parent_id"] = parent.ID
		}
		if a.Status != "" {
			body["status"] = a.Status
		}
		if a.AssigneeUserID != nil {
			body["assignee_user_id"] = *a.AssigneeUserID
		}
		var out any
		if err := c.post(ctx, "/v1/tickets", body, &out); err != nil {
			return nil, nil, err
		}
		return asJSON(out), nil, nil
	}
}

type getTicketArgs struct {
	ID string `json:"id" jsonschema:"ticket id (ULID) or display ID like FOO-14"`
}

func toolGetTicket(c *Client) sdk.ToolHandlerFor[getTicketArgs, any] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, a getTicketArgs) (*sdk.CallToolResult, any, error) {
		if a.ID == "" {
			return nil, nil, fmt.Errorf("id is required")
		}
		var out any
		if err := c.get(ctx, "/v1/tickets/"+escapePathSeg(a.ID), &out); err != nil {
			return nil, nil, err
		}
		return asJSON(out), nil, nil
	}
}

type searchTicketsArgs struct {
	Where   map[string]any `json:"where,omitempty" jsonschema:"filter object; see tool description for operators"`
	Limit   int            `json:"limit,omitempty" jsonschema:"max results (default 50, cap 200)"`
	OrderBy []any          `json:"order_by,omitempty" jsonschema:"optional sort: array of {field, dir} objects where dir is asc or desc"`
}

func toolSearchTickets(c *Client) sdk.ToolHandlerFor[searchTicketsArgs, any] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, a searchTicketsArgs) (*sdk.CallToolResult, any, error) {
		body := map[string]any{}
		if len(a.Where) > 0 {
			body["where"] = a.Where
		}
		if a.Limit > 0 {
			body["limit"] = a.Limit
		}
		if len(a.OrderBy) > 0 {
			body["order_by"] = a.OrderBy
		}
		var out any
		if err := c.post(ctx, "/v1/tickets/search", body, &out); err != nil {
			return nil, nil, err
		}
		return asJSON(out), nil, nil
	}
}

type updateTicketsArgs struct {
	Ticket  string         `json:"ticket,omitempty" jsonschema:"single ticket id or display ID; mutually exclusive with where"`
	Where   map[string]any `json:"where,omitempty" jsonschema:"filter object for bulk update; mutually exclusive with ticket"`
	Changes map[string]any `json:"changes" jsonschema:"fields to set; required"`
}

func toolUpdateTickets(c *Client) sdk.ToolHandlerFor[updateTicketsArgs, any] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, a updateTicketsArgs) (*sdk.CallToolResult, any, error) {
		if len(a.Changes) == 0 {
			return nil, nil, fmt.Errorf("changes is required")
		}
		if (a.Ticket == "") == (len(a.Where) == 0) {
			return nil, nil, fmt.Errorf("exactly one of ticket or where must be set")
		}
		var out any
		if a.Ticket != "" {
			if err := c.patch(ctx, "/v1/tickets/"+escapePathSeg(a.Ticket), a.Changes, &out); err != nil {
				return nil, nil, err
			}
		} else {
			if err := c.patch(ctx, "/v1/tickets", map[string]any{
				"where": a.Where,
				"set":   a.Changes,
			}, &out); err != nil {
				return nil, nil, err
			}
		}
		return asJSON(out), nil, nil
	}
}

type deleteTicketsArgs struct {
	Ticket string         `json:"ticket,omitempty"`
	Where  map[string]any `json:"where,omitempty"`
}

func toolDeleteTickets(c *Client) sdk.ToolHandlerFor[deleteTicketsArgs, any] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, a deleteTicketsArgs) (*sdk.CallToolResult, any, error) {
		if (a.Ticket == "") == (len(a.Where) == 0) {
			return nil, nil, fmt.Errorf("exactly one of ticket or where must be set")
		}
		var out any
		if a.Ticket != "" {
			if err := c.deleteReq(ctx, "/v1/tickets/"+escapePathSeg(a.Ticket), nil, &out); err != nil {
				return nil, nil, err
			}
		} else {
			if err := c.deleteReq(ctx, "/v1/tickets", map[string]any{"where": a.Where}, &out); err != nil {
				return nil, nil, err
			}
		}
		return asJSON(out), nil, nil
	}
}

type addCommentArgs struct {
	Ticket       string `json:"ticket" jsonschema:"ticket id (ULID) or display ID"`
	Body         string `json:"body" jsonschema:"comment text; required and must be non-empty"`
	AuthorUserID string `json:"author_user_id,omitempty" jsonschema:"only needed when calling as unrestricted admin"`
}

func toolAddComment(c *Client) sdk.ToolHandlerFor[addCommentArgs, any] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, a addCommentArgs) (*sdk.CallToolResult, any, error) {
		if a.Ticket == "" || a.Body == "" {
			return nil, nil, fmt.Errorf("ticket and body are required")
		}
		// Resolve display ID to ticket ULID.
		var ticket struct {
			ID string `json:"id"`
		}
		if err := c.get(ctx, "/v1/tickets/"+escapePathSeg(a.Ticket), &ticket); err != nil {
			return nil, nil, fmt.Errorf("resolve ticket %q: %w", a.Ticket, err)
		}
		body := map[string]any{
			"ticket_id": ticket.ID,
			"body":      a.Body,
		}
		if a.AuthorUserID != "" {
			body["author_user_id"] = a.AuthorUserID
		}
		var out any
		if err := c.post(ctx, "/v1/comments", body, &out); err != nil {
			return nil, nil, err
		}
		return asJSON(out), nil, nil
	}
}

type listCommentsArgs struct {
	Ticket string `json:"ticket" jsonschema:"ticket id or display ID"`
	Limit  int    `json:"limit,omitempty"`
}

func toolListComments(c *Client) sdk.ToolHandlerFor[listCommentsArgs, any] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, a listCommentsArgs) (*sdk.CallToolResult, any, error) {
		if a.Ticket == "" {
			return nil, nil, fmt.Errorf("ticket is required")
		}
		var ticket struct {
			ID string `json:"id"`
		}
		if err := c.get(ctx, "/v1/tickets/"+escapePathSeg(a.Ticket), &ticket); err != nil {
			return nil, nil, fmt.Errorf("resolve ticket %q: %w", a.Ticket, err)
		}
		body := map[string]any{
			"where": map[string]any{"ticket_id": ticket.ID},
		}
		if a.Limit > 0 {
			body["limit"] = a.Limit
		}
		var out any
		if err := c.post(ctx, "/v1/comments/search", body, &out); err != nil {
			return nil, nil, err
		}
		return asJSON(out), nil, nil
	}
}

type watchArgs struct {
	Name       string         `json:"name,omitempty"`
	Resource   string         `json:"resource" jsonschema:"one of: tickets, comments, projects, teams, team_members, users"`
	EventTypes []string       `json:"event_types" jsonschema:"array of: create, update, delete, restore"`
	Where      map[string]any `json:"where" jsonschema:"filter evaluated against each affected row"`
	MaxFires   *int           `json:"max_fires,omitempty" jsonschema:"stop after N events (useful for one-time watches)"`
	ExpiresAt  string         `json:"expires_at,omitempty" jsonschema:"RFC3339 cutoff; after this the subscription stops firing"`
	WebhookURL string         `json:"webhook_url,omitempty" jsonschema:"localhost URL to push events to as they fire (optional)"`
}

func toolWatch(c *Client) sdk.ToolHandlerFor[watchArgs, any] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, a watchArgs) (*sdk.CallToolResult, any, error) {
		if a.Resource == "" || len(a.EventTypes) == 0 {
			return nil, nil, fmt.Errorf("resource and event_types are required")
		}
		body := map[string]any{
			"resource":    a.Resource,
			"event_types": a.EventTypes,
			"where":       a.Where,
		}
		if a.Name != "" {
			body["name"] = a.Name
		}
		if a.MaxFires != nil {
			body["max_fires"] = *a.MaxFires
		}
		if a.ExpiresAt != "" {
			body["expires_at"] = a.ExpiresAt
		}
		if a.WebhookURL != "" {
			body["webhook_url"] = a.WebhookURL
		}
		var out any
		if err := c.post(ctx, "/v1/subscriptions", body, &out); err != nil {
			return nil, nil, err
		}
		return asJSON(out), nil, nil
	}
}

type unwatchArgs struct {
	ID string `json:"id" jsonschema:"subscription id"`
}

func toolUnwatch(c *Client) sdk.ToolHandlerFor[unwatchArgs, any] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, a unwatchArgs) (*sdk.CallToolResult, any, error) {
		if a.ID == "" {
			return nil, nil, fmt.Errorf("id is required")
		}
		var out any
		if err := c.deleteReq(ctx, "/v1/subscriptions/"+escapePathSeg(a.ID), nil, &out); err != nil {
			return nil, nil, err
		}
		return asJSON(out), nil, nil
	}
}

type readEventsArgs struct {
	SubscriptionID string `json:"subscription_id"`
	After          int64  `json:"after,omitempty" jsonschema:"sequence number of last processed event; start at 0"`
	Limit          int    `json:"limit,omitempty"`
}

func toolReadEvents(c *Client) sdk.ToolHandlerFor[readEventsArgs, any] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, a readEventsArgs) (*sdk.CallToolResult, any, error) {
		if a.SubscriptionID == "" {
			return nil, nil, fmt.Errorf("subscription_id is required")
		}
		path := fmt.Sprintf("/v1/subscriptions/%s/events?after=%d", escapePathSeg(a.SubscriptionID), a.After)
		if a.Limit > 0 {
			path += fmt.Sprintf("&limit=%d", a.Limit)
		}
		var out any
		if err := c.get(ctx, path, &out); err != nil {
			return nil, nil, err
		}
		return asJSON(out), nil, nil
	}
}

type ackEventsArgs struct {
	SubscriptionID string `json:"subscription_id"`
	Cursor         int64  `json:"cursor" jsonschema:"ack events with sequence <= cursor"`
}

func toolAckEvents(c *Client) sdk.ToolHandlerFor[ackEventsArgs, any] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, a ackEventsArgs) (*sdk.CallToolResult, any, error) {
		if a.SubscriptionID == "" {
			return nil, nil, fmt.Errorf("subscription_id is required")
		}
		var out any
		if err := c.post(ctx,
			"/v1/subscriptions/"+escapePathSeg(a.SubscriptionID)+"/ack",
			map[string]any{"cursor": a.Cursor}, &out); err != nil {
			return nil, nil, err
		}
		return asJSON(out), nil, nil
	}
}

// --- helper: format any value as pretty-printed JSON inside a TextContent ---

func asJSON(v any) *sdk.CallToolResult {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return &sdk.CallToolResult{
			Content: []sdk.Content{&sdk.TextContent{Text: fmt.Sprintf("error formatting response: %v", err)}},
			IsError: true,
		}
	}
	return &sdk.CallToolResult{
		Content: []sdk.Content{&sdk.TextContent{Text: string(b)}},
	}
}
