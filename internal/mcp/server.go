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

// registerTools wires every MCP tool handler into the server. The tool set is
// intentionally comprehensive rather than minimal: tasks127 is headless, with
// no human-facing UI, so the agent is the only path the operator has into the
// system. That means the agent needs to be able to do everything, including
// administrative setup. API key management is the one exception left out,
// because minting new admin keys has enough blast radius that keeping it
// operator-only is worth a small amount of friction.
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

	sdk.AddTool(s, &sdk.Tool{
		Name: "list_team_members",
		Description: "List team memberships, optionally filtered to a specific team. Use this " +
			"to see who belongs to a team. team accepts a team_id or a 3-letter key.",
	}, toolListTeamMembers(c))

	sdk.AddTool(s, &sdk.Tool{
		Name: "get_config",
		Description: "Return deployment configuration the agent is allowed to see. Currently " +
			"the only field is default_webhook_url, which is either a URL the operator has " +
			"configured as the agent's webhook receiver, or null if the operator did not set " +
			"one. Call this before 'watch' when you need to know where to send webhook " +
			"deliveries in this deployment; if it is null, fall back to asking the operator.",
	}, toolGetConfig(c))

	// --- user management -------------------------------------------------------

	sdk.AddTool(s, &sdk.Tool{
		Name: "create_user",
		Description: "Create a new user. Admin only. name and email are required and email must " +
			"be unique among active users. A common reason to call this is to provision an " +
			"identity for an agent or other service account that needs to author comments or " +
			"be assigned tickets.",
	}, toolCreateUser(c))

	sdk.AddTool(s, &sdk.Tool{
		Name: "update_user",
		Description: "Update a user's name or email. A user can update themselves (use " +
			"on_behalf_of if calling as admin); admin-unrestricted can update anyone. Pass " +
			"only the fields you want to change.",
	}, toolUpdateUser(c))

	sdk.AddTool(s, &sdk.Tool{
		Name: "delete_user",
		Description: "Soft-delete a user. Admin only. The user's tickets and comments remain " +
			"in place; only the user row is marked deleted. Reversible via restore_user.",
	}, toolDeleteUser(c))

	sdk.AddTool(s, &sdk.Tool{
		Name: "restore_user",
		Description: "Restore a soft-deleted user. Admin only. Takes the raw user id, since " +
			"deleted users are not findable through search. Returns 409 if the user's email " +
			"now collides with an active user.",
	}, toolRestoreUser(c))

	// --- team management -------------------------------------------------------

	sdk.AddTool(s, &sdk.Tool{
		Name: "create_team",
		Description: "Create a new team. Admin only. key must be exactly 3 uppercase letters " +
			"and globally unique across teams and projects combined (the same key cannot be " +
			"used by both a team and a project). The key becomes the prefix for display IDs " +
			"of tickets in this team that are not inside a project.",
	}, toolCreateTeam(c))

	sdk.AddTool(s, &sdk.Tool{
		Name: "update_team",
		Description: "Rename a team. Admin only. team accepts id or 3-letter key. The key " +
			"itself is immutable, because changing it would break all existing ticket " +
			"display IDs that use that key.",
	}, toolUpdateTeam(c))

	sdk.AddTool(s, &sdk.Tool{
		Name: "delete_team",
		Description: "Soft-delete a team. Admin only. Existing tickets and projects remain " +
			"associated with the team; the team row is simply hidden. Reversible via " +
			"restore_team.",
	}, toolDeleteTeam(c))

	sdk.AddTool(s, &sdk.Tool{
		Name: "restore_team",
		Description: "Restore a soft-deleted team. Admin only. Takes the raw team id (not the " +
			"3-letter key), since deleted teams are not findable through search.",
	}, toolRestoreTeam(c))

	// --- project management ----------------------------------------------------

	sdk.AddTool(s, &sdk.Tool{
		Name: "create_project",
		Description: "Create a new project within a team. Admin only. key must be 3 uppercase " +
			"letters, globally unique across teams and projects. team accepts id or team key.",
	}, toolCreateProject(c))

	sdk.AddTool(s, &sdk.Tool{
		Name: "update_project",
		Description: "Rename a project. Admin only. project accepts id or 3-letter key.",
	}, toolUpdateProject(c))

	sdk.AddTool(s, &sdk.Tool{
		Name: "delete_project",
		Description: "Soft-delete a project. Admin only. Tickets in the project keep their " +
			"display IDs (the project's key is baked into the ticket at creation time), so " +
			"deleting a project does not break ticket references.",
	}, toolDeleteProject(c))

	sdk.AddTool(s, &sdk.Tool{
		Name: "restore_project",
		Description: "Restore a soft-deleted project. Admin only. Takes the raw project id.",
	}, toolRestoreProject(c))

	// --- team membership -------------------------------------------------------

	sdk.AddTool(s, &sdk.Tool{
		Name: "add_team_member",
		Description: "Add a user to a team. Admin only. team accepts id or 3-letter key. " +
			"This is how visibility is extended: a user sees only the teams they belong to.",
	}, toolAddTeamMember(c))

	sdk.AddTool(s, &sdk.Tool{
		Name: "remove_team_member",
		Description: "Remove a user from a team. Admin only. Takes team (id or key) and user " +
			"(user_id). Looks up the membership row internally, so you do not need to know " +
			"the membership id.",
	}, toolRemoveTeamMember(c))

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
			"ID). For bulk, pass where (a filter). Reversible via restore_ticket; deleted tickets are " +
			"simply hidden from default queries.",
	}, toolDeleteTickets(c))

	sdk.AddTool(s, &sdk.Tool{
		Name: "restore_ticket",
		Description: "Restore a soft-deleted ticket. Takes ticket id (ULID) or display ID. " +
			"Caller must have access to the ticket's team.",
	}, toolRestoreTicket(c))

	// --- comments --------------------------------------------------------------

	sdk.AddTool(s, &sdk.Tool{
		Name: "add_comment",
		Description: "Add a comment to a ticket. ticket accepts either a ULID or a display ID " +
			"(e.g. FOO-14). If you are authenticated as a user, the comment is authored as you; " +
			"if you are an unscoped admin, pass author_user_id to specify who the comment is from.",
	}, toolAddComment(c))

	sdk.AddTool(s, &sdk.Tool{
		Name: "list_comments",
		Description: "List comments on a specific ticket. ticket accepts ULID or display ID. " +
			"Defaults to chronological order (oldest first), which is the natural reading order " +
			"for a discussion. Pass order_by to override.",
	}, toolListComments(c))

	sdk.AddTool(s, &sdk.Tool{
		Name: "edit_comment",
		Description: "Edit a comment's body. The comment's author can edit their own; " +
			"admin-unrestricted can edit anyone's. Pass on_behalf_of to edit as a specific user.",
	}, toolEditComment(c))

	sdk.AddTool(s, &sdk.Tool{
		Name:        "delete_comment",
		Description: "Soft-delete a comment. Author or admin-unrestricted can do it. Reversible via restore_comment.",
	}, toolDeleteComment(c))

	sdk.AddTool(s, &sdk.Tool{
		Name:        "restore_comment",
		Description: "Restore a soft-deleted comment. Author or admin-unrestricted can do it.",
	}, toolRestoreComment(c))

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

	sdk.AddTool(s, &sdk.Tool{
		Name: "get_subscription",
		Description: "Read one subscription by id. Returns full details including webhook_url, " +
			"but never the webhook_secret (which is only shown once at creation).",
	}, toolGetSubscription(c))

	sdk.AddTool(s, &sdk.Tool{
		Name: "list_subscriptions",
		Description: "List subscriptions visible to the caller. Normally returns only your own " +
			"subscriptions; admin-unrestricted sees all. Useful to inspect what is currently " +
			"being watched and to find subscription ids for unwatch, read_events, or " +
			"list_deliveries.",
	}, toolListSubscriptions(c))

	sdk.AddTool(s, &sdk.Tool{
		Name: "list_deliveries",
		Description: "List recent webhook delivery attempts for a subscription, with status " +
			"codes, error messages, and retry scheduling. This is the right tool when push " +
			"notifications are not arriving and you need to debug why. Readable by the " +
			"subscription's owner or by admin-unrestricted.",
	}, toolListDeliveries(c))
}

// --- tool handlers --------------------------------------------------------
//
// Each tool is a function returning the SDK's handler shape. Handlers make a
// REST call via the client and format the response as a TextContent block
// containing pretty-printed JSON. Errors (including APIError) propagate
// unchanged; the SDK will convert them into an MCP tool-error response.

func toolGetConfig(c *Client) sdk.ToolHandlerFor[noArgs, any] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, _ noArgs) (*sdk.CallToolResult, any, error) {
		var out any
		if err := c.get(ctx, "/v1/config", &out); err != nil {
			return nil, nil, err
		}
		return asJSON(out), nil, nil
	}
}

type noArgs struct{}

type whoamiArgs struct {
	OnBehalfOf string `json:"on_behalf_of,omitempty" jsonschema:"optional user_id to check the effective identity for; useful to verify on-behalf-of scoping works"`
}

func toolWhoami(c *Client) sdk.ToolHandlerFor[whoamiArgs, any] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, a whoamiArgs) (*sdk.CallToolResult, any, error) {
		var out any
		if err := c.get(ctx, "/v1/whoami", &out, oboOpts(a.OnBehalfOf)...); err != nil {
			return nil, nil, err
		}
		return asJSON(out), nil, nil
	}
}

type listTeamsArgs struct {
	Limit      int    `json:"limit,omitempty" jsonschema:"maximum teams to return (default 50, max 200)"`
	OnBehalfOf string `json:"on_behalf_of,omitempty" jsonschema:"optional user_id to scope the list to what that user can see"`
}

func toolListTeams(c *Client) sdk.ToolHandlerFor[listTeamsArgs, any] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, a listTeamsArgs) (*sdk.CallToolResult, any, error) {
		body := map[string]any{}
		if a.Limit > 0 {
			body["limit"] = a.Limit
		}
		var out any
		if err := c.post(ctx, "/v1/teams/search", body, &out, oboOpts(a.OnBehalfOf)...); err != nil {
			return nil, nil, err
		}
		return asJSON(out), nil, nil
	}
}

type listProjectsArgs struct {
	Team       string `json:"team,omitempty" jsonschema:"optional team_id (ULID) or three-letter key to filter to one team's projects"`
	Limit      int    `json:"limit,omitempty" jsonschema:"maximum projects to return"`
	OnBehalfOf string `json:"on_behalf_of,omitempty" jsonschema:"optional user_id to scope to what that user can see"`
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
		if err := c.post(ctx, "/v1/projects/search", body, &out, oboOpts(a.OnBehalfOf)...); err != nil {
			return nil, nil, err
		}
		return asJSON(out), nil, nil
	}
}

type searchUsersArgs struct {
	Contains   string `json:"contains,omitempty" jsonschema:"substring to match against name or email (case-insensitive)"`
	Limit      int    `json:"limit,omitempty"`
	OnBehalfOf string `json:"on_behalf_of,omitempty" jsonschema:"optional user_id to scope visibility"`
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
		if err := c.post(ctx, "/v1/users/search", body, &out, oboOpts(a.OnBehalfOf)...); err != nil {
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
	OnBehalfOf     string  `json:"on_behalf_of,omitempty" jsonschema:"optional user_id to create the ticket as; see tool descriptions"`
}

func toolCreateTicket(c *Client) sdk.ToolHandlerFor[createTicketArgs, any] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, a createTicketArgs) (*sdk.CallToolResult, any, error) {
		if a.Team == "" || a.Title == "" {
			return nil, nil, fmt.Errorf("team and title are required")
		}
		// Resolve keys using the ACTING principal's visibility, not admin's.
		teamID, err := c.resolveTeamID(ctx, a.Team, oboOpts(a.OnBehalfOf)...)
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
			projectID, err := c.resolveProjectID(ctx, a.Project, oboOpts(a.OnBehalfOf)...)
			if err != nil {
				return nil, nil, err
			}
			body["project_id"] = projectID
		}
		if a.Parent != "" {
			var parent struct {
				ID string `json:"id"`
			}
			if err := c.get(ctx, "/v1/tickets/"+escapePathSeg(a.Parent), &parent, oboOpts(a.OnBehalfOf)...); err != nil {
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
		if err := c.post(ctx, "/v1/tickets", body, &out, oboOpts(a.OnBehalfOf)...); err != nil {
			return nil, nil, err
		}
		return asJSON(out), nil, nil
	}
}

type getTicketArgs struct {
	ID         string `json:"id" jsonschema:"ticket id (ULID) or display ID like FOO-14"`
	OnBehalfOf string `json:"on_behalf_of,omitempty" jsonschema:"optional user_id to scope visibility"`
}

func toolGetTicket(c *Client) sdk.ToolHandlerFor[getTicketArgs, any] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, a getTicketArgs) (*sdk.CallToolResult, any, error) {
		if a.ID == "" {
			return nil, nil, fmt.Errorf("id is required")
		}
		var out any
		if err := c.get(ctx, "/v1/tickets/"+escapePathSeg(a.ID), &out, oboOpts(a.OnBehalfOf)...); err != nil {
			return nil, nil, err
		}
		return asJSON(out), nil, nil
	}
}

type searchTicketsArgs struct {
	Where      map[string]any `json:"where,omitempty" jsonschema:"filter object; see tool description for operators"`
	Limit      int            `json:"limit,omitempty" jsonschema:"max results (default 50, cap 200)"`
	OrderBy    []any          `json:"order_by,omitempty" jsonschema:"optional sort: array of {field, dir} objects where dir is asc or desc"`
	OnBehalfOf string         `json:"on_behalf_of,omitempty" jsonschema:"optional user_id to scope visibility"`
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
		if err := c.post(ctx, "/v1/tickets/search", body, &out, oboOpts(a.OnBehalfOf)...); err != nil {
			return nil, nil, err
		}
		return asJSON(out), nil, nil
	}
}

type updateTicketsArgs struct {
	Ticket     string         `json:"ticket,omitempty" jsonschema:"single ticket id or display ID; mutually exclusive with where"`
	Where      map[string]any `json:"where,omitempty" jsonschema:"filter object for bulk update; mutually exclusive with ticket"`
	Changes    map[string]any `json:"changes" jsonschema:"fields to set; required"`
	OnBehalfOf string         `json:"on_behalf_of,omitempty"`
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
			if err := c.patch(ctx, "/v1/tickets/"+escapePathSeg(a.Ticket), a.Changes, &out, oboOpts(a.OnBehalfOf)...); err != nil {
				return nil, nil, err
			}
		} else {
			if err := c.patch(ctx, "/v1/tickets", map[string]any{
				"where": a.Where,
				"set":   a.Changes,
			}, &out, oboOpts(a.OnBehalfOf)...); err != nil {
				return nil, nil, err
			}
		}
		return asJSON(out), nil, nil
	}
}

type deleteTicketsArgs struct {
	Ticket     string         `json:"ticket,omitempty"`
	Where      map[string]any `json:"where,omitempty"`
	OnBehalfOf string         `json:"on_behalf_of,omitempty"`
}

func toolDeleteTickets(c *Client) sdk.ToolHandlerFor[deleteTicketsArgs, any] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, a deleteTicketsArgs) (*sdk.CallToolResult, any, error) {
		if (a.Ticket == "") == (len(a.Where) == 0) {
			return nil, nil, fmt.Errorf("exactly one of ticket or where must be set")
		}
		var out any
		if a.Ticket != "" {
			if err := c.deleteReq(ctx, "/v1/tickets/"+escapePathSeg(a.Ticket), nil, &out, oboOpts(a.OnBehalfOf)...); err != nil {
				return nil, nil, err
			}
		} else {
			if err := c.deleteReq(ctx, "/v1/tickets", map[string]any{"where": a.Where}, &out, oboOpts(a.OnBehalfOf)...); err != nil {
				return nil, nil, err
			}
		}
		return asJSON(out), nil, nil
	}
}

type addCommentArgs struct {
	Ticket       string `json:"ticket" jsonschema:"ticket id (ULID) or display ID"`
	Body         string `json:"body" jsonschema:"comment text; required and must be non-empty"`
	AuthorUserID string `json:"author_user_id,omitempty" jsonschema:"only needed when calling as unrestricted admin without on_behalf_of"`
	OnBehalfOf   string `json:"on_behalf_of,omitempty" jsonschema:"set to author the comment as this user; the REST API will record that user as author automatically"`
}

func toolAddComment(c *Client) sdk.ToolHandlerFor[addCommentArgs, any] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, a addCommentArgs) (*sdk.CallToolResult, any, error) {
		if a.Ticket == "" || a.Body == "" {
			return nil, nil, fmt.Errorf("ticket and body are required")
		}
		var ticket struct {
			ID string `json:"id"`
		}
		if err := c.get(ctx, "/v1/tickets/"+escapePathSeg(a.Ticket), &ticket, oboOpts(a.OnBehalfOf)...); err != nil {
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
		if err := c.post(ctx, "/v1/comments", body, &out, oboOpts(a.OnBehalfOf)...); err != nil {
			return nil, nil, err
		}
		return asJSON(out), nil, nil
	}
}

type listCommentsArgs struct {
	Ticket     string `json:"ticket" jsonschema:"ticket id or display ID"`
	Limit      int    `json:"limit,omitempty"`
	OrderBy    []any  `json:"order_by,omitempty" jsonschema:"optional sort override; defaults to created_at ASC (oldest first) for natural reading order"`
	OnBehalfOf string `json:"on_behalf_of,omitempty"`
}

func toolListComments(c *Client) sdk.ToolHandlerFor[listCommentsArgs, any] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, a listCommentsArgs) (*sdk.CallToolResult, any, error) {
		if a.Ticket == "" {
			return nil, nil, fmt.Errorf("ticket is required")
		}
		var ticket struct {
			ID string `json:"id"`
		}
		if err := c.get(ctx, "/v1/tickets/"+escapePathSeg(a.Ticket), &ticket, oboOpts(a.OnBehalfOf)...); err != nil {
			return nil, nil, fmt.Errorf("resolve ticket %q: %w", a.Ticket, err)
		}
		body := map[string]any{
			"where": map[string]any{"ticket_id": ticket.ID},
		}
		// Default to chronological order so the agent reads the discussion
		// oldest-first. The REST search default is created_at DESC which is
		// the wrong direction for "read what was said" workflows.
		if len(a.OrderBy) > 0 {
			body["order_by"] = a.OrderBy
		} else {
			body["order_by"] = []any{map[string]any{"field": "created_at", "dir": "asc"}}
		}
		if a.Limit > 0 {
			body["limit"] = a.Limit
		}
		var out any
		if err := c.post(ctx, "/v1/comments/search", body, &out, oboOpts(a.OnBehalfOf)...); err != nil {
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
	WebhookURL string         `json:"webhook_url,omitempty" jsonschema:"URL to push events to as they fire. Must be loopback by default; operator can add hostnames via TASKS127_WEBHOOK_ALLOWED_HOSTS"`
	OnBehalfOf string         `json:"on_behalf_of,omitempty" jsonschema:"scope the subscription to a specific user; only matching events visible to that user will fire"`
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
		if err := c.post(ctx, "/v1/subscriptions", body, &out, oboOpts(a.OnBehalfOf)...); err != nil {
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

// --- user management ---

type createUserArgs struct {
	Name  string `json:"name" jsonschema:"the user's display name"`
	Email string `json:"email" jsonschema:"unique email; required"`
}

func toolCreateUser(c *Client) sdk.ToolHandlerFor[createUserArgs, any] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, a createUserArgs) (*sdk.CallToolResult, any, error) {
		if a.Name == "" || a.Email == "" {
			return nil, nil, fmt.Errorf("name and email are required")
		}
		var out any
		if err := c.post(ctx, "/v1/users",
			map[string]any{"name": a.Name, "email": a.Email}, &out); err != nil {
			return nil, nil, err
		}
		return asJSON(out), nil, nil
	}
}

type updateUserArgs struct {
	ID         string  `json:"id" jsonschema:"user id (ULID)"`
	Name       *string `json:"name,omitempty"`
	Email      *string `json:"email,omitempty"`
	OnBehalfOf string  `json:"on_behalf_of,omitempty"`
}

func toolUpdateUser(c *Client) sdk.ToolHandlerFor[updateUserArgs, any] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, a updateUserArgs) (*sdk.CallToolResult, any, error) {
		if a.ID == "" {
			return nil, nil, fmt.Errorf("id is required")
		}
		body := map[string]any{}
		if a.Name != nil {
			body["name"] = *a.Name
		}
		if a.Email != nil {
			body["email"] = *a.Email
		}
		if len(body) == 0 {
			return nil, nil, fmt.Errorf("at least one of name or email must be provided")
		}
		var out any
		if err := c.patch(ctx, "/v1/users/"+escapePathSeg(a.ID), body, &out, oboOpts(a.OnBehalfOf)...); err != nil {
			return nil, nil, err
		}
		return asJSON(out), nil, nil
	}
}

type idArgs struct {
	ID string `json:"id"`
}

func toolDeleteUser(c *Client) sdk.ToolHandlerFor[idArgs, any] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, a idArgs) (*sdk.CallToolResult, any, error) {
		if a.ID == "" {
			return nil, nil, fmt.Errorf("id is required")
		}
		var out any
		if err := c.deleteReq(ctx, "/v1/users/"+escapePathSeg(a.ID), nil, &out); err != nil {
			return nil, nil, err
		}
		return asJSON(out), nil, nil
	}
}

func toolRestoreUser(c *Client) sdk.ToolHandlerFor[idArgs, any] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, a idArgs) (*sdk.CallToolResult, any, error) {
		if a.ID == "" {
			return nil, nil, fmt.Errorf("id is required")
		}
		var out any
		if err := c.post(ctx, "/v1/users/"+escapePathSeg(a.ID)+"/restore", nil, &out); err != nil {
			return nil, nil, err
		}
		return asJSON(out), nil, nil
	}
}

// --- team management ---

type createTeamArgs struct {
	Key  string `json:"key" jsonschema:"exactly 3 uppercase letters; globally unique across teams and projects"`
	Name string `json:"name"`
}

func toolCreateTeam(c *Client) sdk.ToolHandlerFor[createTeamArgs, any] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, a createTeamArgs) (*sdk.CallToolResult, any, error) {
		if a.Key == "" || a.Name == "" {
			return nil, nil, fmt.Errorf("key and name are required")
		}
		var out any
		if err := c.post(ctx, "/v1/teams",
			map[string]any{"key": a.Key, "name": a.Name}, &out); err != nil {
			return nil, nil, err
		}
		return asJSON(out), nil, nil
	}
}

type updateTeamArgs struct {
	Team string `json:"team" jsonschema:"team id or 3-letter key"`
	Name string `json:"name"`
}

func toolUpdateTeam(c *Client) sdk.ToolHandlerFor[updateTeamArgs, any] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, a updateTeamArgs) (*sdk.CallToolResult, any, error) {
		if a.Team == "" || a.Name == "" {
			return nil, nil, fmt.Errorf("team and name are required")
		}
		id, err := c.resolveTeamID(ctx, a.Team)
		if err != nil {
			return nil, nil, err
		}
		var out any
		if err := c.patch(ctx, "/v1/teams/"+escapePathSeg(id),
			map[string]any{"name": a.Name}, &out); err != nil {
			return nil, nil, err
		}
		return asJSON(out), nil, nil
	}
}

type teamArgs struct {
	Team string `json:"team" jsonschema:"team id or 3-letter key"`
}

func toolDeleteTeam(c *Client) sdk.ToolHandlerFor[teamArgs, any] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, a teamArgs) (*sdk.CallToolResult, any, error) {
		if a.Team == "" {
			return nil, nil, fmt.Errorf("team is required")
		}
		id, err := c.resolveTeamID(ctx, a.Team)
		if err != nil {
			return nil, nil, err
		}
		var out any
		if err := c.deleteReq(ctx, "/v1/teams/"+escapePathSeg(id), nil, &out); err != nil {
			return nil, nil, err
		}
		return asJSON(out), nil, nil
	}
}

func toolRestoreTeam(c *Client) sdk.ToolHandlerFor[idArgs, any] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, a idArgs) (*sdk.CallToolResult, any, error) {
		if a.ID == "" {
			return nil, nil, fmt.Errorf("id is required")
		}
		var out any
		if err := c.post(ctx, "/v1/teams/"+escapePathSeg(a.ID)+"/restore", nil, &out); err != nil {
			return nil, nil, err
		}
		return asJSON(out), nil, nil
	}
}

// --- project management ---

type createProjectArgs struct {
	Key  string `json:"key" jsonschema:"3 uppercase letters; globally unique across teams and projects"`
	Team string `json:"team" jsonschema:"team id or 3-letter key"`
	Name string `json:"name"`
}

func toolCreateProject(c *Client) sdk.ToolHandlerFor[createProjectArgs, any] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, a createProjectArgs) (*sdk.CallToolResult, any, error) {
		if a.Key == "" || a.Team == "" || a.Name == "" {
			return nil, nil, fmt.Errorf("key, team, and name are required")
		}
		teamID, err := c.resolveTeamID(ctx, a.Team)
		if err != nil {
			return nil, nil, err
		}
		var out any
		if err := c.post(ctx, "/v1/projects",
			map[string]any{"key": a.Key, "team_id": teamID, "name": a.Name}, &out); err != nil {
			return nil, nil, err
		}
		return asJSON(out), nil, nil
	}
}

type updateProjectArgs struct {
	Project string `json:"project" jsonschema:"project id or 3-letter key"`
	Name    string `json:"name"`
}

func toolUpdateProject(c *Client) sdk.ToolHandlerFor[updateProjectArgs, any] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, a updateProjectArgs) (*sdk.CallToolResult, any, error) {
		if a.Project == "" || a.Name == "" {
			return nil, nil, fmt.Errorf("project and name are required")
		}
		id, err := c.resolveProjectID(ctx, a.Project)
		if err != nil {
			return nil, nil, err
		}
		var out any
		if err := c.patch(ctx, "/v1/projects/"+escapePathSeg(id),
			map[string]any{"name": a.Name}, &out); err != nil {
			return nil, nil, err
		}
		return asJSON(out), nil, nil
	}
}

type projectArgs struct {
	Project string `json:"project" jsonschema:"project id or 3-letter key"`
}

func toolDeleteProject(c *Client) sdk.ToolHandlerFor[projectArgs, any] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, a projectArgs) (*sdk.CallToolResult, any, error) {
		if a.Project == "" {
			return nil, nil, fmt.Errorf("project is required")
		}
		id, err := c.resolveProjectID(ctx, a.Project)
		if err != nil {
			return nil, nil, err
		}
		var out any
		if err := c.deleteReq(ctx, "/v1/projects/"+escapePathSeg(id), nil, &out); err != nil {
			return nil, nil, err
		}
		return asJSON(out), nil, nil
	}
}

func toolRestoreProject(c *Client) sdk.ToolHandlerFor[idArgs, any] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, a idArgs) (*sdk.CallToolResult, any, error) {
		if a.ID == "" {
			return nil, nil, fmt.Errorf("id is required")
		}
		var out any
		if err := c.post(ctx, "/v1/projects/"+escapePathSeg(a.ID)+"/restore", nil, &out); err != nil {
			return nil, nil, err
		}
		return asJSON(out), nil, nil
	}
}

// --- team membership ---

type teamMemberArgs struct {
	Team string `json:"team" jsonschema:"team id or 3-letter key"`
	User string `json:"user" jsonschema:"user id"`
}

func toolAddTeamMember(c *Client) sdk.ToolHandlerFor[teamMemberArgs, any] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, a teamMemberArgs) (*sdk.CallToolResult, any, error) {
		if a.Team == "" || a.User == "" {
			return nil, nil, fmt.Errorf("team and user are required")
		}
		teamID, err := c.resolveTeamID(ctx, a.Team)
		if err != nil {
			return nil, nil, err
		}
		var out any
		if err := c.post(ctx, "/v1/team-members",
			map[string]any{"team_id": teamID, "user_id": a.User}, &out); err != nil {
			return nil, nil, err
		}
		return asJSON(out), nil, nil
	}
}

func toolRemoveTeamMember(c *Client) sdk.ToolHandlerFor[teamMemberArgs, any] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, a teamMemberArgs) (*sdk.CallToolResult, any, error) {
		if a.Team == "" || a.User == "" {
			return nil, nil, fmt.Errorf("team and user are required")
		}
		teamID, err := c.resolveTeamID(ctx, a.Team)
		if err != nil {
			return nil, nil, err
		}
		// Find the membership row so we can delete it by id.
		var search struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := c.post(ctx, "/v1/team-members/search", map[string]any{
			"where": map[string]any{"team_id": teamID, "user_id": a.User},
			"limit": 1,
		}, &search); err != nil {
			return nil, nil, err
		}
		if len(search.Data) == 0 {
			return nil, nil, fmt.Errorf("no active membership found for that team and user")
		}
		var out any
		if err := c.deleteReq(ctx, "/v1/team-members/"+escapePathSeg(search.Data[0].ID), nil, &out); err != nil {
			return nil, nil, err
		}
		return asJSON(out), nil, nil
	}
}

type listTeamMembersArgs struct {
	Team       string `json:"team,omitempty" jsonschema:"optional team id or 3-letter key"`
	Limit      int    `json:"limit,omitempty"`
	OnBehalfOf string `json:"on_behalf_of,omitempty"`
}

func toolListTeamMembers(c *Client) sdk.ToolHandlerFor[listTeamMembersArgs, any] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, a listTeamMembersArgs) (*sdk.CallToolResult, any, error) {
		body := map[string]any{}
		if a.Team != "" {
			teamID, err := c.resolveTeamID(ctx, a.Team, oboOpts(a.OnBehalfOf)...)
			if err != nil {
				return nil, nil, err
			}
			body["where"] = map[string]any{"team_id": teamID}
		}
		if a.Limit > 0 {
			body["limit"] = a.Limit
		}
		var out any
		if err := c.post(ctx, "/v1/team-members/search", body, &out, oboOpts(a.OnBehalfOf)...); err != nil {
			return nil, nil, err
		}
		return asJSON(out), nil, nil
	}
}

// --- ticket restore ---

type restoreTicketArgs struct {
	ID         string `json:"id" jsonschema:"ticket id (ULID) or display ID like FOO-14"`
	OnBehalfOf string `json:"on_behalf_of,omitempty"`
}

func toolRestoreTicket(c *Client) sdk.ToolHandlerFor[restoreTicketArgs, any] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, a restoreTicketArgs) (*sdk.CallToolResult, any, error) {
		if a.ID == "" {
			return nil, nil, fmt.Errorf("id is required")
		}
		var out any
		if err := c.post(ctx, "/v1/tickets/"+escapePathSeg(a.ID)+"/restore", nil, &out, oboOpts(a.OnBehalfOf)...); err != nil {
			return nil, nil, err
		}
		return asJSON(out), nil, nil
	}
}

// --- comment editing ---

type editCommentArgs struct {
	ID         string `json:"id" jsonschema:"comment id"`
	Body       string `json:"body" jsonschema:"new comment text; required and must be non-empty"`
	OnBehalfOf string `json:"on_behalf_of,omitempty"`
}

func toolEditComment(c *Client) sdk.ToolHandlerFor[editCommentArgs, any] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, a editCommentArgs) (*sdk.CallToolResult, any, error) {
		if a.ID == "" || a.Body == "" {
			return nil, nil, fmt.Errorf("id and body are required")
		}
		var out any
		if err := c.patch(ctx, "/v1/comments/"+escapePathSeg(a.ID),
			map[string]any{"body": a.Body}, &out, oboOpts(a.OnBehalfOf)...); err != nil {
			return nil, nil, err
		}
		return asJSON(out), nil, nil
	}
}

type commentIDArgs struct {
	ID         string `json:"id"`
	OnBehalfOf string `json:"on_behalf_of,omitempty"`
}

func toolDeleteComment(c *Client) sdk.ToolHandlerFor[commentIDArgs, any] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, a commentIDArgs) (*sdk.CallToolResult, any, error) {
		if a.ID == "" {
			return nil, nil, fmt.Errorf("id is required")
		}
		var out any
		if err := c.deleteReq(ctx, "/v1/comments/"+escapePathSeg(a.ID), nil, &out, oboOpts(a.OnBehalfOf)...); err != nil {
			return nil, nil, err
		}
		return asJSON(out), nil, nil
	}
}

func toolRestoreComment(c *Client) sdk.ToolHandlerFor[commentIDArgs, any] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, a commentIDArgs) (*sdk.CallToolResult, any, error) {
		if a.ID == "" {
			return nil, nil, fmt.Errorf("id is required")
		}
		var out any
		if err := c.post(ctx, "/v1/comments/"+escapePathSeg(a.ID)+"/restore", nil, &out, oboOpts(a.OnBehalfOf)...); err != nil {
			return nil, nil, err
		}
		return asJSON(out), nil, nil
	}
}

// --- subscription introspection ---

type subIDArgs struct {
	ID         string `json:"id" jsonschema:"subscription id"`
	OnBehalfOf string `json:"on_behalf_of,omitempty"`
}

func toolGetSubscription(c *Client) sdk.ToolHandlerFor[subIDArgs, any] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, a subIDArgs) (*sdk.CallToolResult, any, error) {
		if a.ID == "" {
			return nil, nil, fmt.Errorf("id is required")
		}
		var out any
		if err := c.get(ctx, "/v1/subscriptions/"+escapePathSeg(a.ID), &out, oboOpts(a.OnBehalfOf)...); err != nil {
			return nil, nil, err
		}
		return asJSON(out), nil, nil
	}
}

type listSubscriptionsArgs struct {
	Where      map[string]any `json:"where,omitempty" jsonschema:"optional filter; filterable fields include resource, name, fire_count, expires_at"`
	Limit      int            `json:"limit,omitempty"`
	OnBehalfOf string         `json:"on_behalf_of,omitempty"`
}

func toolListSubscriptions(c *Client) sdk.ToolHandlerFor[listSubscriptionsArgs, any] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, a listSubscriptionsArgs) (*sdk.CallToolResult, any, error) {
		body := map[string]any{}
		if len(a.Where) > 0 {
			body["where"] = a.Where
		}
		if a.Limit > 0 {
			body["limit"] = a.Limit
		}
		var out any
		if err := c.post(ctx, "/v1/subscriptions/search", body, &out, oboOpts(a.OnBehalfOf)...); err != nil {
			return nil, nil, err
		}
		return asJSON(out), nil, nil
	}
}

func toolListDeliveries(c *Client) sdk.ToolHandlerFor[subIDArgs, any] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, a subIDArgs) (*sdk.CallToolResult, any, error) {
		if a.ID == "" {
			return nil, nil, fmt.Errorf("id is required")
		}
		var out any
		if err := c.get(ctx, "/v1/subscriptions/"+escapePathSeg(a.ID)+"/deliveries", &out, oboOpts(a.OnBehalfOf)...); err != nil {
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
